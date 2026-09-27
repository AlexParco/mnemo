package integration

import (
	"encoding/json"
	"fmt"
	"os/exec"
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

// AddClaude installs the plugin.
func AddClaude(run func(args ...string) (string, error)) (Result, error) {
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
		if notThere(out) {
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
	if !existed || !gjson.Valid(content) || !gjson.Get(content, "mcpServers.mnemo").Exists() {
		result.Outcome = Nothing
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

func notThere(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "not found") || strings.Contains(lower, "not installed")
}
