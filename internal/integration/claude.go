package integration

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Claude Code is the only one of the three that is not a file mnemo edits. It
// has a plugin, and the plugin carries more than the tools: the mailbox monitor,
// the save reminder and the /mnemo:* commands. So the normal path runs `claude`
// and lets it install, and the file path exists for the case where that cannot
// happen — it gives the tools and nothing else, and says so.

// Marketplace is where the plugin comes from.
const (
	Marketplace = "AlexParco/mnemo"
	PluginRef   = "mnemo@mnemo"
)

// ClaudeCommands are what a person runs by hand when mnemo cannot.
func ClaudeCommands() string {
	return "claude plugin marketplace add " + Marketplace + "\nclaude plugin install " + PluginRef
}

// InstalledVersion is the version of the plugin Claude Code has, and whether it
// could be worked out at all.
func InstalledVersion(run func(args ...string) (string, error)) (string, bool) {
	if run == nil {
		run = claudeCommand
	}
	out, err := run("plugin", "list")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "mnemo") {
			continue
		}
		// The version is the first thing on the line that looks like one.
		for _, field := range strings.Fields(strings.NewReplacer("(", " ", ")", " ", ",", " ").Replace(line)) {
			if version := strings.TrimPrefix(field, "v"); looksLikeVersion(version) {
				return version, true
			}
		}
	}
	return "", false
}

func looksLikeVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// AddClaude installs the plugin and checks that what got installed is this
// binary's plugin.
//
// The marketplace is a name, and a name can serve a different implementation
// than the one running here — it does today, while this rewrite is unpublished.
// Installing that and reporting success would hand a chat a mnemo that keeps its
// memory somewhere else, and nothing downstream would notice.
func AddClaude(version string, run func(args ...string) (string, error)) (Result, error) {
	result := Result{Tool: "claude"}
	if run == nil {
		run = claudeCommand
	}
	if !Installed("claude") {
		result.Outcome = Skipped
		result.Advice = "Once Claude Code is installed, run:\n" + ClaudeCommands()
		return result, nil
	}
	if !OnPath() {
		// The plugin runs `mnemo` by name, because a plugin cannot know an
		// absolute path. Said now rather than discovered as a broken chat.
		result.Advice = "warning: mnemo is not on the PATH. The plugin runs it by name, so add its directory " +
			"to your PATH or the tools will not start."
	}

	if out, err := run("plugin", "marketplace", "add", Marketplace); err != nil && !alreadyThere(out) {
		result.Outcome, result.Detail = Refused, "adding the marketplace failed"
		result.Advice = strings.TrimSpace(out) + "\nRun these by hand:\n" + ClaudeCommands()
		return result, nil
	}
	out, err := run("plugin", "install", PluginRef)
	if err != nil && alreadyThere(out) {
		// Installed already: updating is what "add" means the second time.
		out, err = run("plugin", "update", PluginRef)
	}
	if err != nil {
		result.Outcome, result.Detail = Refused, "installing the plugin failed"
		result.Advice = strings.TrimSpace(out) + "\nRun these by hand:\n" + ClaudeCommands()
		return result, nil
	}
	if installed, known := InstalledVersion(run); known && version != "dev" && installed != strings.TrimPrefix(version, "v") {
		result.Outcome, result.Detail = Refused, "the marketplace served plugin "+installed
		result.Advice = fmt.Sprintf("This binary is %s, and the plugin that was installed is %s — a different "+
			"implementation, which does not use this binary and does not read the store this binary reads.\n"+
			"Remove it with: claude plugin uninstall %s\n"+
			"Then wire this binary into one project instead:\n"+
			"  mnemo mcp add claude --path <project>/.mcp.json", version, installed, PluginRef)
		return result, nil
	}
	result.Outcome, result.Detail = Added, PluginRef
	return result, nil
}

// RemoveClaude uninstalls the plugin.
func RemoveClaude(run func(args ...string) (string, error)) (Result, error) {
	result := Result{Tool: "claude"}
	if run == nil {
		run = claudeCommand
	}
	if !Installed("claude") {
		result.Outcome = Nothing
		return result, nil
	}
	out, err := run("plugin", "uninstall", PluginRef)
	if err != nil {
		// Asked, not guessed from the words in the output: plenty of real
		// failures contain "not found", and reporting those as nothing to remove
		// left the plugin installed with nobody the wiser.
		if !RegisteredClaude(run) {
			result.Outcome = Nothing
			return result, nil
		}
		result.Outcome, result.Detail = Refused, "uninstalling the plugin failed"
		result.Advice = strings.TrimSpace(out) + "\nRun: claude plugin uninstall " + PluginRef
		return result, nil
	}
	result.Outcome, result.Detail = Removed, PluginRef
	return result, nil
}

// AddClaudeFile writes an .mcp.json-shaped file, for the case where the plugin
// cannot be installed. It gives the tools and nothing else.
func AddClaudeFile(path, mnemo string) (Result, error) {
	result := Result{Tool: "claude", Detail: path}
	content, existed, err := read(path)
	if err != nil {
		return result, err
	}

	entry := map[string]any{
		"command": mnemo,
		"args":    []string{"serve"},
		"env":     map[string]string{"MNEMO_TOOL": "claude"},
	}
	if !existed || strings.TrimSpace(content) == "" {
		fresh, err := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"mnemo": entry}}, "", "  ")
		if err != nil {
			return result, err
		}
		if err := write(path, append(fresh, '\n')); err != nil {
			return result, err
		}
		result.Outcome = Added
		result.Advice = "This gives the tools only: no mailbox monitor and no /mnemo:* commands. " +
			"For those, install the plugin with: " + ClaudeCommands()
		return result, nil
	}
	if !gjson.Valid(content) {
		result.Outcome = Refused
		result.Advice = "This file is not plain JSON, so mnemo left it alone."
		return result, nil
	}
	if existing := gjson.Get(content, "mcpServers.mnemo"); existing.Exists() && !oursClaude(existing) {
		// .mcp.json is usually committed, so this entry is often somebody else's
		// — with their own store, their own flags.
		result.Outcome = Refused
		result.Advice = "There is already an mcpServers.mnemo in this file that does not look like mnemo's own " +
			"entry: it may be a teammate's, with its own store.\nRemove it and run this again, or leave it and " +
			"mnemo will not manage this file."
		return result, nil
	}

	updated := content
	for key, value := range map[string]any{
		"mcpServers.mnemo.command":        mnemo,
		"mcpServers.mnemo.args":           []string{"serve"},
		"mcpServers.mnemo.env.MNEMO_TOOL": "claude",
	} {
		updated, err = sjson.Set(updated, key, value)
		if err != nil {
			return result, fmt.Errorf("editing %s: %w", path, err)
		}
	}
	if updated == content {
		result.Outcome = Current
		return result, nil
	}
	if err := backup(path); err != nil {
		return result, err
	}
	if err := write(path, []byte(updated)); err != nil {
		return result, err
	}
	result.Outcome = Added
	result.Advice = "This gives the tools only: no mailbox monitor and no /mnemo:* commands."
	return result, nil
}

// RemoveClaudeFile takes the entry out of an .mcp.json-shaped file.
func RemoveClaudeFile(path string) (Result, error) {
	result := Result{Tool: "claude", Detail: path}
	content, existed, err := read(path)
	if err != nil {
		return result, err
	}
	existing := gjson.Get(content, "mcpServers.mnemo")
	if !existed || !gjson.Valid(content) || !existing.Exists() {
		result.Outcome = Nothing
		return result, nil
	}
	if !oursClaude(existing) {
		result.Outcome = Nothing
		result.Advice = "The mcpServers.mnemo in this file does not look like mnemo's own entry, so it was left alone."
		return result, nil
	}
	if err := backup(path); err != nil {
		return result, err
	}
	updated, err := sjson.Delete(content, "mcpServers.mnemo")
	if err != nil {
		return result, err
	}
	if err := write(path, []byte(updated)); err != nil {
		return result, err
	}
	result.Outcome = Removed
	return result, nil
}

// RegisteredClaude reports whether the plugin is installed and enabled.
func RegisteredClaude(run func(args ...string) (string, error)) bool {
	if run == nil {
		run = claudeCommand
	}
	out, err := run("plugin", "list")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "mnemo") && !strings.Contains(strings.ToLower(line), "disabled") {
			return true
		}
	}
	return false
}

// oursClaude reports whether an entry looks like one mnemo wrote: a mnemo
// executable with exactly `serve` and nothing else. Anything with extra flags or
// a different command belongs to whoever put it there.
func oursClaude(entry gjson.Result) bool {
	args := entry.Get("args").Array()
	if len(args) != 1 || args[0].String() != "serve" {
		return false
	}
	command := entry.Get("command").String()
	base := filepath.Base(command)
	return base == "mnemo" || base == "mnemo.exe"
}

func claudeCommand(args ...string) (string, error) {
	cmd := exec.Command("claude", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// alreadyThere reports whether a command failed only because the thing it would
// create is already there.
func alreadyThere(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "already") || strings.Contains(lower, "exists")
}
