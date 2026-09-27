package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// These tests are all one worry: mnemo is editing a file somebody else owns.
// Every case below is a way to damage it — dropping a comment, reflowing it,
// taking over an entry mnemo did not write, leaving it unparseable.

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func slurp(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(content)
}

const mnemo = "/somewhere/bin/mnemo"

// Codex keeps comments in its config, and every TOML library drops them when it
// rewrites a file. That is why the edit is textual, and this is the test that
// says so.
func TestCodexKeepsWhatThePersonWrote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "" +
		"# my own notes, do not lose these\n" +
		"model = \"a-model\"\n" +
		"\n" +
		"[tui]\n" +
		"theme = \"dark\"  # trailing comment\n"
	put(t, path, original)

	result, err := AddCodex(path, mnemo)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Added {
		t.Fatalf("result = %+v", result)
	}

	after := slurp(t, path)
	for _, want := range []string{
		"# my own notes, do not lose these", "model = \"a-model\"",
		"[tui]", "theme = \"dark\"  # trailing comment",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("the edit lost %q:\n%s", want, after)
		}
	}
	// A copy of the file as it was, kept before the first change.
	if kept := slurp(t, path+backupSuffix); kept != original {
		t.Errorf("the backup is not the original:\n%s", kept)
	}

	// What was written has to be valid TOML that says what it should, or Codex
	// will not start.
	var parsed struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(after, &parsed); err != nil {
		t.Fatalf("the edited file is not valid TOML: %v\n%s", err, after)
	}
	entry := parsed.MCPServers["mnemo"]
	if entry.Command != mnemo || strings.Join(entry.Args, ",") != "serve" || entry.Env["MNEMO_TOOL"] != "codex" {
		t.Errorf("the entry is %+v", entry)
	}
}

func TestCodexIsIdempotentAndReversible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	put(t, path, "model = \"a-model\"\n")

	if _, err := AddCodex(path, mnemo); err != nil {
		t.Fatal(err)
	}
	once := slurp(t, path)
	result, err := AddCodex(path, mnemo)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Current {
		t.Errorf("adding twice = %+v, want it to say nothing changed", result)
	}
	if slurp(t, path) != once {
		t.Error("adding twice changed the file")
	}

	// A binary that moved: the entry is rewritten with the new path and nothing
	// else changes.
	moved, err := AddCodex(path, "/elsewhere/mnemo")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Outcome != Added {
		t.Errorf("a moved binary = %+v", moved)
	}
	after := slurp(t, path)
	if !strings.Contains(after, "/elsewhere/mnemo") || strings.Contains(after, mnemo) {
		t.Errorf("the path was not rewritten:\n%s", after)
	}
	if strings.Count(after, "[mcp_servers.mnemo]") != 1 {
		t.Errorf("the entry was duplicated:\n%s", after)
	}

	if _, err := RemoveCodex(path); err != nil {
		t.Fatal(err)
	}
	if got := slurp(t, path); strings.Contains(got, "mnemo") {
		t.Errorf("removing left something behind:\n%s", got)
	}
	if got := slurp(t, path); !strings.Contains(got, "model = \"a-model\"") {
		t.Errorf("removing took the user's own setting:\n%s", got)
	}
	again, err := RemoveCodex(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Outcome != Nothing {
		t.Errorf("removing twice = %+v", again)
	}
}

// An entry a person wrote themselves is theirs. Taking it over would silently
// change a configuration mnemo did not make.
func TestCodexWillNotTakeOverSomebodyElsesEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "[mcp_servers.mnemo]\ncommand = \"/my/own/build/mnemo\"\nargs = [\"serve\", \"--verbose\"]\n"
	put(t, path, original)

	result, err := AddCodex(path, mnemo)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Refused {
		t.Fatalf("result = %+v, want a refusal", result)
	}
	if !strings.Contains(result.Advice, "mnemo did not write") {
		t.Errorf("the advice reads:\n%s", result.Advice)
	}
	if slurp(t, path) != original {
		t.Error("the file was changed anyway")
	}

	// Removing leaves it alone too, and says so.
	removed, err := RemoveCodex(path)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Outcome != Nothing || !strings.Contains(removed.Advice, "left alone") {
		t.Errorf("removing = %+v", removed)
	}
	if slurp(t, path) != original {
		t.Error("removing changed somebody else's entry")
	}
}

func TestOpencode(t *testing.T) {
	dir := t.TempDir()

	t.Run("a missing file is created with its schema", func(t *testing.T) {
		path := filepath.Join(dir, "fresh.json")
		if _, err := AddOpencode(path, mnemo); err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			Schema string `json:"$schema"`
			MCP    map[string]struct {
				Type        string            `json:"type"`
				Command     []string          `json:"command"`
				Enabled     bool              `json:"enabled"`
				Environment map[string]string `json:"environment"`
			} `json:"mcp"`
		}
		if err := json.Unmarshal([]byte(slurp(t, path)), &parsed); err != nil {
			t.Fatalf("what was written is not JSON: %v", err)
		}
		if parsed.Schema != opencodeSchema {
			t.Errorf("schema is %q", parsed.Schema)
		}
		entry := parsed.MCP["mnemo"]
		if entry.Type != "local" || !entry.Enabled || entry.Environment["MNEMO_TOOL"] != "opencode" {
			t.Errorf("entry = %+v", entry)
		}
		if strings.Join(entry.Command, " ") != mnemo+" serve" {
			t.Errorf("command = %v", entry.Command)
		}
	})

	t.Run("an existing file keeps its own keys", func(t *testing.T) {
		path := filepath.Join(dir, "existing.json")
		put(t, path, "{\n  \"theme\": \"gruvbox\",\n  \"mcp\": {\n    \"other\": {\"type\": \"local\", \"command\": [\"other\"]}\n  }\n}\n")
		if _, err := AddOpencode(path, mnemo); err != nil {
			t.Fatal(err)
		}
		after := slurp(t, path)
		for _, want := range []string{"gruvbox", "\"other\"", "mnemo"} {
			if !strings.Contains(after, want) {
				t.Errorf("the edit lost %q:\n%s", want, after)
			}
		}
		if !json.Valid([]byte(after)) {
			t.Errorf("the edit produced invalid JSON:\n%s", after)
		}
	})

	t.Run("a file that is not plain JSON is left alone", func(t *testing.T) {
		path := filepath.Join(dir, "commented.json")
		original := "{\n  // opencode accepts this, mnemo does not understand it\n  \"theme\": \"x\"\n}\n"
		put(t, path, original)
		result, err := AddOpencode(path, mnemo)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != Refused {
			t.Errorf("result = %+v", result)
		}
		if slurp(t, path) != original {
			t.Error("the file was rewritten")
		}
		// Refusing is only acceptable if the user is told what to paste.
		if !strings.Contains(result.Advice, "\"mnemo\"") {
			t.Errorf("the advice does not show the entry:\n%s", result.Advice)
		}
	})

	t.Run("somebody else's entry", func(t *testing.T) {
		path := filepath.Join(dir, "theirs.json")
		original := "{\"mcp\":{\"mnemo\":{\"type\":\"local\",\"command\":[\"/their/thing\"]}}}"
		put(t, path, original)
		result, err := AddOpencode(path, mnemo)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != Refused {
			t.Errorf("result = %+v", result)
		}
		if slurp(t, path) != original {
			t.Error("somebody else's entry was overwritten")
		}
	})
}

func TestClaudeFileGivesTheToolsAndSaysWhatItDoesNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")

	result, err := AddClaudeFile(path, mnemo)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Added {
		t.Fatalf("result = %+v", result)
	}
	// Whoever takes this path has to know what they are not getting.
	for _, want := range []string{"tools only", "/mnemo:*", "plugin"} {
		if !strings.Contains(result.Advice, want) {
			t.Errorf("the advice does not mention %q:\n%s", want, result.Advice)
		}
	}

	var parsed struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(slurp(t, path)), &parsed); err != nil {
		t.Fatalf("what was written is not JSON: %v", err)
	}
	entry := parsed.MCPServers["mnemo"]
	if entry.Command != mnemo || strings.Join(entry.Args, ",") != "serve" || entry.Env["MNEMO_TOOL"] != "claude" {
		t.Errorf("entry = %+v", entry)
	}

	removed, err := RemoveClaudeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Outcome != Removed {
		t.Errorf("removing = %+v", removed)
	}
	if strings.Contains(slurp(t, path), "mnemo") {
		t.Error("removing left the entry behind")
	}
}

// The plugin is installed by running `claude`, so the failures worth testing are
// the ones where that goes wrong: the user has to be left with the two commands
// they can run themselves.
func TestClaudePluginFailuresSayWhatToRun(t *testing.T) {
	t.Run("the marketplace is already added", func(t *testing.T) {
		var ran []string
		result, err := AddClaude(func(args ...string) (string, error) {
			ran = append(ran, strings.Join(args, " "))
			if args[1] == "marketplace" {
				return "marketplace mnemo already exists", fmt.Errorf("exit 1")
			}
			return "installed", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !Installed("claude") {
			t.Skip("claude is not on this machine, so the installed check short-circuits")
		}
		if result.Outcome != Added {
			t.Errorf("result = %+v; an existing marketplace counts as success", result)
		}
		if len(ran) != 2 {
			t.Errorf("commands run: %q", ran)
		}
	})

	t.Run("installing fails", func(t *testing.T) {
		if !Installed("claude") {
			t.Skip("claude is not on this machine")
		}
		result, err := AddClaude(func(args ...string) (string, error) {
			if args[1] == "install" {
				return "network unreachable", fmt.Errorf("exit 1")
			}
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != Refused {
			t.Errorf("result = %+v", result)
		}
		for _, want := range []string{"network unreachable", "claude plugin marketplace add", "claude plugin install"} {
			if !strings.Contains(result.Advice, want) {
				t.Errorf("the advice does not carry %q:\n%s", want, result.Advice)
			}
		}
	})
}

// Every file mnemo touches is replaced by a rename, so a tool reading it while
// mnemo writes never sees half a file. Proved with a hard link, because nothing
// else tells a rename from a write in place afterwards.
func TestEditsReplaceTheFileByRenaming(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	put(t, path, "model = \"a-model\"\n")
	witness := path + ".witness"
	if err := os.Link(path, witness); err != nil {
		t.Skipf("this filesystem has no hard links: %v", err)
	}

	if _, err := AddCodex(path, mnemo); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(slurp(t, path), "mnemo") {
		t.Fatal("the edit did not take")
	}
	if strings.Contains(slurp(t, witness), "mnemo") {
		t.Error("the file was written in place, so a tool could read half of it")
	}
}

// The copy is of the file as it was before mnemo ever touched it, and only that.
// A second run must not replace it with mnemo's own output: the run that needs
// the backup is usually the one after the one that made it.
func TestTheBackupIsOfTheFileAsThePersonWroteIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "# the one I want back\nmodel = \"a-model\"\n"
	put(t, path, original)

	if _, err := AddCodex(path, mnemo); err != nil {
		t.Fatal(err)
	}
	if kept := slurp(t, path+backupSuffix); kept != original {
		t.Fatalf("the first backup is not the original:\n%s", kept)
	}

	// The person edits their file, and mnemo runs again — after an update, say.
	edited := original + "\n[tui]\ntheme = \"dark\"\n"
	put(t, path, edited)
	if _, err := AddCodex(path, "/elsewhere/mnemo"); err != nil {
		t.Fatal(err)
	}

	if kept := slurp(t, path+backupSuffix); kept != original {
		t.Errorf("the backup was replaced; it should still be the file as it was before mnemo "+
			"first touched it:\n%s", kept)
	}
	if after := slurp(t, path); !strings.Contains(after, "theme = \"dark\"") {
		t.Errorf("the second run lost the person's later edit:\n%s", after)
	}
}
