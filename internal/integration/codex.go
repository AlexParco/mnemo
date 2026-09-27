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
		updated = before + entry + after
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
	cleaned := strings.TrimRight(before, "\n") + "\n" + strings.TrimLeft(after, "\n")
	if strings.TrimSpace(cleaned) == "" {
		cleaned = ""
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

// split cuts a file around mnemo's block.
func split(content string) (before, after string, found bool) {
	start := strings.Index(content, codexBegin)
	if start == -1 {
		return content, "", false
	}
	rest := content[start:]
	end := strings.Index(rest, codexEnd)
	if end == -1 {
		return content, "", false
	}
	return content[:start], rest[end+len(codexEnd):], true
}

func middleOf(content string) string {
	before, after, found := split(content)
	if !found {
		return ""
	}
	return content[len(before) : len(content)-len(after)]
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
