package gitx

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The secret scan. Nothing leaves the machine without passing it.
//
// Only added lines are scanned. With the hub's branch as the base, those are
// exactly what this push would publish; removed and context lines are already on
// the hub, and flagging them would block every future push over an old leak.

// emptyTree is git's constant for a tree with nothing in it. It is the base when
// nothing has been pushed yet, so a first push scans the whole history.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// A Rule is one thing worth stopping a push for.
type Rule struct {
	Name    string
	Pattern *regexp.Regexp
}

// Rules are deliberately few. A false positive blocks the user from syncing
// their own memory, so each one has to earn its place.
var Rules = []Rule{
	{"private key block", regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"AWS access key id", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`(?i)\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}\b`)},
	{"GitHub fine-grained token", regexp.MustCompile(`(?i)\bgithub_pat_[A-Za-z0-9_]{20,}\b`)},
	{"Slack token", regexp.MustCompile(`(?i)\bxox[baprs]-[A-Za-z0-9-]{8,}`)},
	{"URL with embedded credentials", regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^/:@\s]+:[^/@\s]+@`)},
	// The value has to be an unbroken token of at least twelve characters. A
	// looser rule fires on ordinary engineering prose, such as a note saying to
	// rotate a token every fifteen minutes.
	{"credential assignment", regexp.MustCompile(`(?i)\b(?:password|passwd|secret|token|api[-_]?key)["'\s]*[:=]["'\s]*([A-Za-z0-9+/_.-]{12,})`)},
}

// A Finding is one place a push would publish something that looks secret.
type Finding struct {
	Rule string
	File string
	// Line is the line number in the file as it would be pushed.
	Line int
	// Excerpt is the line with the match replaced. It never carries the secret.
	Excerpt string
}

// mask keeps the shape and drops the payload. A finding travels through an agent
// transcript, so echoing the secret would be its own leak.
func mask(line, match string) string {
	shown := "…"
	if runes := []rune(match); len(runes) > 8 {
		shown = string(runes[:3]) + "…" + string(runes[len(runes)-2:])
	}
	replaced := strings.Replace(line, match, "[redacted "+strconv.Itoa(len([]rune(match)))+" chars: "+shown+"]", 1)
	return truncate(strings.TrimSpace(replaced), 200)
}

func truncate(s string, limit int) string {
	if runes := []rune(s); len(runes) > limit {
		return string(runes[:limit])
	}
	return s
}

var (
	fileHeader = regexp.MustCompile(`^\+\+\+ b/(.*)$`)
	hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
)

// ScanDiff reads a unified diff and reports what its added lines would publish.
func ScanDiff(diff string) []Finding {
	var findings []Finding
	file := "?"
	line := 0

	for _, raw := range strings.Split(diff, "\n") {
		if m := fileHeader.FindStringSubmatch(raw); m != nil {
			file = m[1]
			continue
		}
		if m := hunkHeader.FindStringSubmatch(raw); m != nil {
			line, _ = strconv.Atoi(m[1])
			continue
		}
		if strings.HasPrefix(raw, "---") || strings.HasPrefix(raw, "+++") {
			continue
		}
		switch {
		case strings.HasPrefix(raw, "+"):
			content := raw[1:]
			for _, rule := range Rules {
				if m := rule.Pattern.FindString(content); m != "" {
					findings = append(findings, Finding{
						Rule:    rule.Name,
						File:    file,
						Line:    line,
						Excerpt: mask(content, m),
					})
				}
			}
			line++
		case strings.HasPrefix(raw, "-"):
			// A removed line is already on the hub; this push does not publish it.
		default:
			line++ // a context line, which advances the new file's numbering
		}
	}
	return findings
}

// ScanContent applies the rules to text that is about to be written, before any
// of it reaches the disk.
//
// The push scan reads a diff, which is the last chance to stop a secret. This is
// the first: by the time a memory is committed the value is already in the
// store, in the repository's history, and in whatever transcript the agent keeps.
// Line numbers are the lines of the content itself.
func ScanContent(file, content string) []Finding {
	var findings []Finding
	for i, line := range strings.Split(content, "\n") {
		for _, rule := range Rules {
			if m := rule.Pattern.FindString(line); m != "" {
				findings = append(findings, Finding{
					Rule:    rule.Name,
					File:    file,
					Line:    i + 1,
					Excerpt: mask(line, m),
				})
			}
		}
	}
	return findings
}

// PushBase is what the hub already has, and so what a push would add to. It
// reports false when there is nothing to push at all.
func (r *Repo) PushBase() (string, bool) {
	if !r.HasCommits() {
		return "", false
	}
	if upstream, ok := r.Upstream(); ok {
		if _, exists := r.Try("rev-parse", "-q", "--verify", upstream); exists {
			return upstream, true
		}
	}
	if _, exists := r.Try("rev-parse", "-q", "--verify", "origin/main"); exists {
		return "origin/main", true
	}
	return emptyTree, true
}

// ScanRange scans everything a push would publish.
func (r *Repo) ScanRange() []Finding {
	base, ok := r.PushBase()
	if !ok {
		return nil
	}
	diff, ok := r.Try("diff", "--no-color", "--no-ext-diff", "--text", base, "HEAD")
	if !ok || diff == "" {
		return nil
	}
	return ScanDiff(diff)
}

// AcknowledgeToken is the value that unblocks this exact set of findings on this
// exact commit.
//
// It is derived, never stored: it survives a restart, it cannot be replayed
// against different content, and it stops working the moment the commits or the
// findings change.
func (r *Repo) AcknowledgeToken(findings []Finding) string {
	head, _ := r.Try("rev-parse", "HEAD")
	keys := make([]string, 0, len(findings))
	for _, f := range findings {
		keys = append(keys, f.Rule+"|"+f.File+"|"+strconv.Itoa(f.Line))
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(head + "\n" + strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}
