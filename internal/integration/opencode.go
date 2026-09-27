package integration

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// opencode's config is JSON. It is edited with a path setter rather than decoded
// and re-encoded, so the rest of the file keeps its own formatting and key order:
// a person who lined their config up by hand should not find it reflowed because
// mnemo added one entry.

// OpencodeFile is where opencode keeps its configuration.
func OpencodeFile() string {
	return filepath.Join(home(), ".config", "opencode", "opencode.json")
}

const opencodeSchema = "https://opencode.ai/config.json"

// AddOpencode writes mnemo's entry under mcp.mnemo.
func AddOpencode(path, mnemo string) (Result, error) {
	result := Result{Tool: "opencode", Detail: path}
	content, existed, err := read(path)
	if err != nil {
		return result, err
	}

	entry := map[string]any{
		"type":        "local",
		"command":     []string{mnemo, "serve"},
		"enabled":     true,
		"environment": map[string]string{"MNEMO_TOOL": "opencode"},
	}
	shown, err := json.MarshalIndent(map[string]any{"mcp": map[string]any{"mnemo": entry}}, "", "  ")
	if err != nil {
		return result, err
	}

	if !existed || strings.TrimSpace(content) == "" {
		fresh, err := json.MarshalIndent(map[string]any{
			"$schema": opencodeSchema,
			"mcp":     map[string]any{"mnemo": entry},
		}, "", "  ")
		if err != nil {
			return result, err
		}
		if err := write(path, append(fresh, '\n')); err != nil {
			return result, err
		}
		result.Outcome = Added
		return result, nil
	}

	if !gjson.Valid(content) {
		// A file with comments, or JSON5. Rewriting it would destroy something
		// opencode accepts and mnemo does not understand.
		result.Outcome = Refused
		result.Advice = "This file is not plain JSON, so mnemo left it alone. Add this by hand:\n" + string(shown)
		return result, nil
	}

	if existing := gjson.Get(content, "mcp.mnemo"); existing.Exists() && !oursOpencode(existing, mnemo) {
		result.Outcome = Refused
		result.Advice = "There is already an mcp.mnemo in this file that does not look like mnemo's.\n" +
			"Remove it and run this again."
		return result, nil
	}

	updated := content
	for key, value := range map[string]any{
		"mcp.mnemo.type":                   entry["type"],
		"mcp.mnemo.command":                entry["command"],
		"mcp.mnemo.enabled":                entry["enabled"],
		"mcp.mnemo.environment.MNEMO_TOOL": "opencode",
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
	return result, nil
}

// RemoveOpencode takes out the entry when mnemo wrote it.
func RemoveOpencode(path string) (Result, error) {
	result := Result{Tool: "opencode", Detail: path}
	content, existed, err := read(path)
	if err != nil {
		return result, err
	}
	if !existed || !gjson.Valid(content) {
		result.Outcome = Nothing
		return result, nil
	}
	existing := gjson.Get(content, "mcp.mnemo")
	if !existing.Exists() {
		result.Outcome = Nothing
		return result, nil
	}
	if !oursOpencode(existing, "") {
		result.Outcome = Nothing
		result.Advice = "The mcp.mnemo in this file does not look like mnemo's, so it was left alone."
		return result, nil
	}
	if err := backup(path); err != nil {
		return result, err
	}
	updated, err := sjson.Delete(content, "mcp.mnemo")
	if err != nil {
		return result, err
	}
	if err := write(path, []byte(updated)); err != nil {
		return result, err
	}
	result.Outcome = Removed
	return result, nil
}

// RegisteredOpencode reports whether the file declares mnemo.
func RegisteredOpencode(path string) bool {
	content, _, err := read(path)
	if err != nil || !gjson.Valid(content) {
		return false
	}
	return gjson.Get(content, "mcp.mnemo").Exists()
}

// oursOpencode reports whether an entry looks like one mnemo wrote: a mnemo
// executable followed by serve. With an empty want, any mnemo will do, which is
// what removing needs after the binary has moved.
func oursOpencode(entry gjson.Result, want string) bool {
	command := entry.Get("command").Array()
	if len(command) < 2 || command[1].String() != "serve" {
		return false
	}
	first := command[0].String()
	if want != "" {
		return first == want || filepath.Base(first) == "mnemo"
	}
	return filepath.Base(first) == "mnemo" || filepath.Base(first) == "mnemo.exe"
}
