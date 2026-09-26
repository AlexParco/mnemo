package criterion

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// The skills are the plugin's copies of these texts. They are what an agent
// actually reads inside Claude Code, so a copy that drifts is a second, quieter
// version of the rules.
const skills = "../../plugin/skills"

var region = regexp.MustCompile(`(?s)<!-- mnemo:rule (\w+) -->\n(.*?)\n<!-- /mnemo:rule -->`)

func TestEveryTextIsThere(t *testing.T) {
	for _, name := range Names() {
		text, ok := Text(name)
		if !ok {
			t.Fatalf("%s is listed by Names but Text does not know it", name)
		}
		if strings.TrimSpace(text) == "" {
			t.Errorf("%s is empty", name)
		}
	}
	if len(Names()) != len(bound) {
		t.Errorf("Names lists %d of %d texts", len(Names()), len(bound))
	}
}

// Nothing may ship with an inclusion left in it: a `{{NAME}}` that reached an
// agent would be a rule it cannot read.
func TestNoTextShipsUnresolved(t *testing.T) {
	for _, name := range Names() {
		text, _ := Text(name)
		if inclusion.MatchString(text) {
			t.Errorf("%s still contains %s", name, inclusion.FindString(text))
		}
	}
}

// The short note a tool returns and the long rule in a guide are the same words.
func TestTheLongRuleCarriesTheShortOne(t *testing.T) {
	if !strings.Contains(ConfirmationRule, ConfirmNote) {
		t.Error("CONFIRMATION_RULE does not contain CONFIRM_NOTE word for word")
	}
	for _, name := range []string{
		"SAVE_CRITERION", "PENDING_CRITERION", "MACHINE_RULE", "CONFLICT_RULE",
		"CONFIRMATION_RULE", "MAILBOX_RULE",
	} {
		text, _ := Text(name)
		if !strings.Contains(Guide, text) {
			t.Errorf("the guide does not carry %s word for word", name)
		}
	}
	// The guide is a rules file: it needs its headings, or it pastes as a wall.
	for _, heading := range []string{
		"# mnemo — persistent project memory", "## Starting work", "## Saving",
		"## Machines", "## Syncing", "## Confirmation", "## Mailbox",
	} {
		if !strings.Contains(Guide, heading) {
			t.Errorf("the guide has no %q section", heading)
		}
	}
}

// A cycle would otherwise recurse until the stack runs out, at startup, with no
// message a person could act on.
func TestACycleIsRefused(t *testing.T) {
	raw := map[string]string{
		"A": "see {{B}}",
		"B": "see {{A}}",
	}
	if _, err := expand("A", raw, nil); err == nil {
		t.Error("a cycle was accepted")
	} else if !strings.Contains(err.Error(), "includes itself") {
		t.Errorf("the message is %q", err)
	}

	// The message has to name the text that closes the loop, not the one the
	// walk happened to start from: it is the file a person has to go and edit.
	deep := map[string]string{"A": "see {{B}}", "B": "see {{C}}", "C": "see {{B}}"}
	_, err := expand("A", deep, nil)
	if err == nil {
		t.Fatal("a cycle below the surface was accepted")
	}
	if !strings.HasPrefix(err.Error(), "B includes itself") {
		t.Errorf("the message blames the wrong text: %q", err)
	}

	if _, err := expand("A", map[string]string{"A": "see {{NOPE}}"}, nil); err == nil {
		t.Error("an unknown inclusion was accepted")
	}
}

func TestInclusionSurvivesNesting(t *testing.T) {
	raw := map[string]string{
		"OUTER":  "before\n{{MIDDLE}}\nafter",
		"MIDDLE": "one {{INNER}} two",
		"INNER":  "deep",
	}
	got, err := expand("OUTER", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "before\none deep two\nafter" {
		t.Errorf("got %q", got)
	}
}

// carriedBy says which skill quotes which rule. A rule an agent needs while it
// works has to be in front of it, so the expectation is per skill and not a
// global "somebody carries it": two skills quote SAVE_CRITERION, and either copy
// could rot while the other keeps the check green.
var carriedBy = map[string][]string{
	"forget":       {"CONFIRM_NOTE"},
	"rename":       {"CONFIRM_NOTE"},
	"mem":          {"SAVE_CRITERION"},
	"load-context": {"MACHINE_RULE"},
	"save-context": {"SAVE_CRITERION", "PENDING_CRITERION", "MACHINE_RULE", "CONFLICT_RULE"},
	"list-context": {},
}

// opener finds the start of a marked region however it is written. The region
// regex is strict on purpose, so anything this finds and that one does not is a
// region hiding from the check rather than a region that is absent.
var opener = regexp.MustCompile(`<!--\s*mnemo:rule`)

// markdownIn lists every .md file of a skill, following a symlink. A directory
// entry's own type is not enough: a skill provided as a symlink reports itself
// as neither a directory nor a file, and skipping it would take the whole skill
// out of the check without a word.
func markdownIn(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() && strings.HasSuffix(path, ".md") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return found
}

// Every marked region in every skill is byte-identical to the text it names.
// This is the test that keeps the plugin honest.
func TestTheSkillsQuoteTheseTextsExactly(t *testing.T) {
	entries, err := os.ReadDir(skills)
	if err != nil {
		t.Fatalf("reading the skills: %v", err)
	}

	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		dir := filepath.Join(skills, name)
		// os.Stat, not e.IsDir: a skill shipped as a symlink is still a skill.
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		seen[name] = true

		found := map[string]int{}
		for _, path := range markdownIn(t, dir) {
			content := readFile(t, path)
			matches := region.FindAllStringSubmatch(content, -1)
			// Anything that looks like the start of a region but was not matched
			// is invisible to the check, which is worse than a wrong copy: a
			// trailing space, a CRLF ending or a misspelt closing marker would
			// take the region out of the comparison in silence.
			if openers := len(opener.FindAllString(content, -1)); openers != len(matches) {
				t.Errorf("%s: %d region(s) start but only %d are readable; a marker is malformed, so a copy is "+
					"hiding from this check", path, openers, len(matches))
			}
			for _, m := range matches {
				ruleName, text := m[1], m[2]
				want, ok := Text(ruleName)
				if !ok {
					t.Errorf("%s quotes %s, which is not a rule text", path, ruleName)
					continue
				}
				if text != want {
					t.Errorf("%s: the copy of %s is not the text.\nskill:\n%s\n\nrules/%s.md:\n%s",
						path, ruleName, text, fileOf(ruleName), want)
				}
				found[ruleName]++
			}
		}

		expected, known := carriedBy[name]
		if !known {
			t.Errorf("the skill %q is not in carriedBy: say which rules it must quote, or none", name)
			continue
		}
		for _, ruleName := range expected {
			if found[ruleName] == 0 {
				t.Errorf("the skill %q no longer quotes %s", name, ruleName)
			}
		}
		for ruleName := range found {
			if !slices.Contains(expected, ruleName) {
				t.Errorf("the skill %q quotes %s, which carriedBy does not expect", name, ruleName)
			}
		}
	}

	for name := range carriedBy {
		if !seen[name] {
			t.Errorf("the skill %q is expected to carry rules but was not found", name)
		}
	}
}

// mnemo's own output has no emojis, and these texts are output.
func TestNoEmojis(t *testing.T) {
	for _, name := range Names() {
		text, _ := Text(name)
		for _, r := range text {
			if r > 0x2000 && !strings.ContainsRune("—–’‘“”…·→ ", r) {
				t.Errorf("%s contains %q (%U)", name, r, r)
			}
		}
	}
}

// The instructions are paid for on every connection, so they stay short.
func TestTheInstructionsStayShort(t *testing.T) {
	if n := len(ServerInstructions); n > 1400 {
		t.Errorf("the instructions are %d bytes; keep them under 1400 or they cost on every connection", n)
	}
	for _, tool := range []string{"mnemo_load_project", "mnemo_push", "mailbox_"} {
		if !strings.Contains(ServerInstructions, tool) {
			t.Errorf("the instructions never mention %s", tool)
		}
	}
}

// The two halves of the binding table, each proved against a rules/ that is
// wrong on purpose. Neither failure can be seen from the outside once the
// program is running: a text with nothing bound to it is written and then never
// shipped, and a variable with no text behind it ships empty. Both have to stop
// the program at startup, so both are checked here.
func TestTheBindingsAndTheFilesMustMatch(t *testing.T) {
	complete := fstest.MapFS{}
	for name := range bound {
		complete["rules/"+fileOf(name)+".md"] = &fstest.MapFile{Data: []byte("text of " + name + "\n")}
	}
	if _, err := read(complete); err != nil {
		t.Fatalf("a complete rules/ was refused: %v", err)
	}

	t.Run("a file nothing is bound to", func(t *testing.T) {
		wrong := fstest.MapFS{"rules/orphan-rule.md": &fstest.MapFile{Data: []byte("nobody reads me\n")}}
		for path, file := range complete {
			wrong[path] = file
		}
		_, err := read(wrong)
		if err == nil {
			t.Fatal("a text with no variable bound to it was accepted, so it would never ship")
		}
		if !strings.Contains(err.Error(), "ORPHAN_RULE") {
			t.Errorf("the message does not name the text: %v", err)
		}
	})

	t.Run("a binding with no file", func(t *testing.T) {
		wrong := fstest.MapFS{}
		for path, file := range complete {
			wrong[path] = file
		}
		delete(wrong, "rules/mailbox-rule.md")
		_, err := read(wrong)
		if err == nil {
			t.Fatal("a missing text was accepted, so the rule would ship empty")
		}
		if !strings.Contains(err.Error(), "MAILBOX_RULE") {
			t.Errorf("the message does not name the text: %v", err)
		}
	})
}

// readFile is a file's contents, or a fatal error naming it.
func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(content)
}
