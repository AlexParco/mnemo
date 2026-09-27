package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Codex's config is TOML that people keep comments in, and every TOML library
// drops comments when it rewrites a file. So the edit is textual, between marker
// comments, and TOML is used only to check afterwards that what was written
// parses and says what it should.

const (
	codexBegin = "# mnemo: begin — managed by `mnemo mcp add`; remove with `mnemo mcp remove codex`"
	codexEnd   = "# mnemo: end"
)

// CodexFile is where Codex keeps its configuration.
func CodexFile() string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return filepath.Join(dir, "config.toml")
	}
	if home() == "" {
		return ""
	}
	return filepath.Join(home(), ".codex", "config.toml")
}

func codexEntry(mnemo string) string {
	return strings.Join([]string{
		codexBegin,
		"[mcp_servers.mnemo]",
		fmt.Sprintf("command = %q", mnemo),
		`args = ["serve"]`,
		`env = { MNEMO_TOOL = "codex" }`,
		`env_vars = ["MNEMO_AGENT"]`,
		codexEnd,
	}, "\n")
}

// AddCodex writes mnemo's entry, replacing its own block if it is already there.
func AddCodex(path, mnemo string) (Result, error) {
	result := Result{Tool: "codex", Detail: path}
	content, existed, err := read(path)
	if err != nil {
		return result, err
	}

	entry := codexEntry(mnemo)
	before, after, marked := split(content)
	if strayBegin(content) {
		result.Outcome = Refused
		result.Advice = "mnemo's block in this file has lost its `" + codexEnd + "` line, so mnemo cannot tell " +
			"where the block ends.\nRestore that line, or delete the block by hand, and run this again."
		return result, nil
	}
	if extraBegin(content) {
		result.Outcome = Refused
		result.Advice = "This file has more than one `" + codexBegin + "` line. mnemo will not guess which " +
			"block is its own.\nLeave one and run this again."
		return result, nil
	}
	if !marked && mentionsCodexMnemo(content) {
		// Somebody else's entry. Rewriting it would silently take over a
		// configuration mnemo did not make.
		result.Outcome, result.Detail = Refused, path
		result.Advice = "There is already an mcp_servers.mnemo in this file that mnemo did not write.\n" +
			"Remove it and run this again, or keep it and mnemo will not manage this file."
		return result, nil
	}

	updated := entry + "\n"
	if marked {
		if strings.TrimSpace(middleOf(content)) == strings.TrimSpace(entry) {
			result.Outcome = Current
			return result, nil
		}
		updated = joinAround(before, entry, after)
	} else if strings.TrimSpace(content) != "" {
		updated = strings.TrimRight(content, "\n") + "\n\n" + entry + "\n"
	}

	// Parsed before anything is written, and checked to say what it should: a
	// textual edit that produced invalid TOML would leave Codex unable to start.
	if err := verifyCodex(updated, mnemo); err != nil {
		result.Outcome, result.Advice = Refused, err.Error()+"\nNothing was written."
		return result, nil
	}
	if existed {
		if err := backup(path); err != nil {
			return result, err
		}
	}
	if err := write(path, []byte(updated)); err != nil {
		return result, err
	}
	result.Outcome = Added
	return result, nil
}

// RemoveCodex takes mnemo's block out and leaves the rest alone.
func RemoveCodex(path string) (Result, error) {
	result := Result{Tool: "codex", Detail: path}
	content, existed, err := read(path)
	if err != nil {
		return result, err
	}
	before, after, marked := split(content)
	if !existed || !marked {
		result.Outcome = Nothing
		if mentionsCodexMnemo(content) {
			result.Advice = "There is an mcp_servers.mnemo in this file that mnemo did not write; it was left alone."
		}
		return result, nil
	}
	if err := backup(path); err != nil {
		return result, err
	}
	cleaned := strings.TrimRight(before, "\n")
	if rest := strings.TrimLeft(after, "\n"); rest != "" {
		cleaned += "\n" + rest
	}
	if strings.TrimSpace(cleaned) == "" {
		cleaned = ""
	} else if !strings.HasSuffix(cleaned, "\n") {
		cleaned += "\n"
	}
	// Checked the same way adding is. Removing used to write blind, so a cut in
	// the wrong place left Codex unable to start and nothing said so.
	if cleaned != "" {
		var parsed map[string]any
		if _, err := toml.Decode(cleaned, &parsed); err != nil {
			result.Outcome = Refused
			result.Advice = fmt.Sprintf("taking mnemo's block out would leave a file that is not valid TOML "+
				"(%v).\nNothing was written; remove the block by hand.", err)
			return result, nil
		}
	}
	if err := write(path, []byte(cleaned)); err != nil {
		return result, err
	}
	result.Outcome = Removed
	return result, nil
}

// RegisteredCodex reports whether mnemo's block is in the global file.
func RegisteredCodex(path string) bool {
	content, _, err := read(path)
	if err != nil {
		return false
	}
	_, _, marked := split(content)
	return marked
}

// split cuts a file around mnemo's block, matching whole lines.
//
// A substring search was wrong in a way that destroyed configurations: a comment
// that merely quotes the marker, or a string value containing it, became the
// start of "mnemo's block", and everything from there to mnemo's real end marker
// was cut out. Markers are lines, so they are matched as lines.
func split(content string) (before, after string, found bool) {
	lines := strings.Split(content, "\n")
	start, end := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		switch {
		case trimmed == codexBegin && start == -1:
			start = i
		case trimmed == codexEnd && start != -1 && end == -1:
			end = i
		}
	}
	if start == -1 || end == -1 {
		return content, "", false
	}
	// And the block has to look like mnemo's. Marker lines can appear inside a
	// multi-line string, where they are whole lines and mean nothing; cutting
	// there would change the value the person wrote.
	if !strings.Contains(strings.Join(lines[start:end+1], "\n"), "[mcp_servers.mnemo]") {
		return content, "", false
	}
	return strings.Join(lines[:start], "\n"), strings.Join(lines[end+1:], "\n"), true
}

// strayBegin reports a begin marker with no end marker after it, which is a
// block somebody half-deleted. Treating it as a foreign entry would tell the
// person mnemo did not write something mnemo wrote, for ever.
func strayBegin(content string) bool {
	if _, _, found := split(content); found {
		return false
	}
	// Only when it is followed by mnemo's entry and no end marker. A marker line
	// inside a string is not a half-deleted block.
	seen := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		switch {
		case trimmed == codexBegin:
			seen = true
		case trimmed == codexEnd:
			seen = false
		case seen && trimmed == "[mcp_servers.mnemo]":
			return true
		}
	}
	return false
}

// extraBegin reports a second begin marker outside the pair, which means a merge
// or a paste left two blocks and cutting on the first would eat what is between.
func extraBegin(content string) bool {
	before, after, found := split(content)
	if !found {
		return false
	}
	for _, line := range strings.Split(before+"\n"+after, "\n") {
		if strings.TrimSpace(strings.TrimSuffix(line, "\r")) == codexBegin {
			return true
		}
	}
	return false
}

// joinAround puts the block back between what surrounded it, keeping one blank
// line's worth of separation and no more.
func joinAround(before, entry, after string) string {
	out := strings.TrimRight(before, "\n")
	if out != "" {
		out += "\n\n"
	}
	out += entry + "\n"
	if rest := strings.TrimLeft(after, "\n"); rest != "" {
		out += "\n" + rest
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// middleOf is mnemo's block as the file currently has it.
func middleOf(content string) string {
	lines := strings.Split(content, "\n")
	start, end := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		switch {
		case trimmed == codexBegin && start == -1:
			start = i
		case trimmed == codexEnd && start != -1 && end == -1:
			end = i
		}
	}
	if start == -1 || end == -1 {
		return ""
	}
	return strings.Join(lines[start:end+1], "\n")
}

// mentionsCodexMnemo reports whether the file declares mnemo outside the markers.
func mentionsCodexMnemo(content string) bool {
	before, after, found := split(content)
	outside := content
	if found {
		outside = before + after
	}
	var parsed map[string]any
	if _, err := toml.Decode(outside, &parsed); err != nil {
		// Unparseable outside our block: treat it as somebody else's, because
		// guessing would mean rewriting a file we cannot read.
		return strings.Contains(outside, "mcp_servers.mnemo")
	}
	servers, _ := parsed["mcp_servers"].(map[string]any)
	_, declared := servers["mnemo"]
	return declared
}

func verifyCodex(content, mnemo string) error {
	var parsed map[string]any
	if _, err := toml.Decode(content, &parsed); err != nil {
		return fmt.Errorf("the edited file is not valid TOML: %w", err)
	}
	servers, ok := parsed["mcp_servers"].(map[string]any)
	if !ok {
		return fmt.Errorf("the edited file has no mcp_servers table")
	}
	entry, ok := servers["mnemo"].(map[string]any)
	if !ok {
		return fmt.Errorf("the edited file has no mcp_servers.mnemo entry")
	}
	if command, _ := entry["command"].(string); command != mnemo {
		return fmt.Errorf("the edited file points at %q, not at %q", entry["command"], mnemo)
	}
	return nil
}
