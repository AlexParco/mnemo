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
		result, err := AddClaude("0.5.0", func(args ...string) (string, error) {
			ran = append(ran, strings.Join(args, " "))
			switch args[1] {
			case "marketplace":
				return "marketplace mnemo already exists", fmt.Errorf("exit 1")
			case "list":
				return "mnemo@mnemo  0.5.0  enabled", nil
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
		if len(ran) != 3 {
			t.Errorf("commands run: %q", ran)
		}
	})

	t.Run("installing fails", func(t *testing.T) {
		if !Installed("claude") {
			t.Skip("claude is not on this machine")
		}
		result, err := AddClaude("0.5.0", func(args ...string) (string, error) {
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

// The copy is of the file as it was immediately before mnemo's last change.
//
// It used to be the first snapshot ever, kept for ever — which is the wrong way
// round. The only thing a backup protects against is the edit mnemo is about to
// make, and by the time somebody reaches for it, a snapshot from the first run
// has lost every change they made since.
func TestTheBackupIsTheFileAsItWasJustBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	put(t, path, "# the first one\nmodel = \"a-model\"\n")

	if _, err := AddCodex(path, mnemo); err != nil {
		t.Fatal(err)
	}

	// The person edits their file, and mnemo runs again — after an update, say.
	edited := "# the first one\nmodel = \"a-model\"\n\n[tui]\ntheme = \"dark\"\n"
	// Keep mnemo's block, which is what a real second run would find.
	withBlock := slurp(t, path)
	edited = strings.Replace(withBlock, "model = \"a-model\"", "model = \"a-model\"\n\n[tui]\ntheme = \"dark\"", 1)
	put(t, path, edited)

	if _, err := AddCodex(path, "/elsewhere/mnemo"); err != nil {
		t.Fatal(err)
	}
	if kept := slurp(t, path+backupSuffix); kept != edited {
		t.Errorf("the backup is not the file as it was just before this change:\n%s", kept)
	}
	if after := slurp(t, path); !strings.Contains(after, "theme = \"dark\"") {
		t.Errorf("the second run lost the person's later edit:\n%s", after)
	}
}

// A comment that merely quotes the marker, or a string value containing it, used
// to become the start of "mnemo's block" — and everything from there to the real
// end marker was cut out. This is the worst thing this package could do.
func TestAQuotedMarkerIsNotAMarker(t *testing.T) {
	for name, original := range map[string]string{
		"in a comment": "model = \"a-model\"\n" +
			"# the block starts with: " + codexBegin + "\n" +
			"approval_policy = \"on-request\"\n\n" +
			"[mcp_servers.other]\ncommand = \"other\"\n\n" +
			codexBegin + "\n[mcp_servers.mnemo]\ncommand = \"/old/mnemo\"\nargs = [\"serve\"]\n" + codexEnd + "\n",
		"in a string": "model = \"a-model\"\n" +
			"note = \"" + codexBegin + " then " + codexEnd + "\"\n" +
			"approval_policy = \"on-request\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			put(t, path, original)

			if _, err := AddCodex(path, mnemo); err != nil {
				t.Fatal(err)
			}
			after := slurp(t, path)
			for _, keep := range []string{"model = \"a-model\"", "approval_policy = \"on-request\""} {
				if !strings.Contains(after, keep) {
					t.Errorf("adding lost %q:\n%s", keep, after)
				}
			}
			var parsed map[string]any
			if _, err := toml.Decode(after, &parsed); err != nil {
				t.Errorf("adding left invalid TOML: %v\n%s", err, after)
			}

			if _, err := RemoveCodex(path); err != nil {
				t.Fatal(err)
			}
			cleaned := slurp(t, path)
			for _, keep := range []string{"model = \"a-model\"", "approval_policy = \"on-request\""} {
				if !strings.Contains(cleaned, keep) {
					t.Errorf("removing lost %q:\n%s", keep, cleaned)
				}
			}
			if _, err := toml.Decode(cleaned, &parsed); err != nil {
				t.Errorf("removing left invalid TOML: %v\n%s", err, cleaned)
			}
		})
	}
}

// A block somebody half-deleted, and a file with two blocks. Neither may be
// guessed at: one used to be reported as an entry mnemo did not write, for ever.
func TestCodexSaysWhatIsWrongWithItsOwnBlock(t *testing.T) {
	t.Run("the end marker is gone", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		put(t, path, "model = \"a-model\"\n\n"+codexBegin+"\n[mcp_servers.mnemo]\ncommand = \"/old/mnemo\"\n")
		result, err := AddCodex(path, mnemo)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != Refused {
			t.Fatalf("result = %+v", result)
		}
		if !strings.Contains(result.Advice, "lost its") {
			t.Errorf("the advice blames the wrong thing:\n%s", result.Advice)
		}
		if strings.Contains(result.Advice, "mnemo did not write") {
			t.Errorf("mnemo called its own block somebody else's:\n%s", result.Advice)
		}
	})

	t.Run("two blocks", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		block := codexBegin + "\n[mcp_servers.mnemo]\ncommand = \"/old/mnemo\"\n" + codexEnd + "\n"
		put(t, path, block+"\nmodel = \"a-model\"\n\n"+codexBegin+"\n")
		result, err := AddCodex(path, mnemo)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != Refused {
			t.Fatalf("result = %+v", result)
		}
		if !strings.Contains(result.Advice, "more than one") {
			t.Errorf("the advice reads:\n%s", result.Advice)
		}
	})
}

// A config fed from a dotfiles repository must keep being fed from it, and a
// 0600 file must not become world-readable: Codex's config can carry keys.
func TestWritingKeepsTheModeAndFollowsASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "codex.toml")
	put(t, real, "model = \"a-model\"\n")
	if err := os.Chmod(real, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this filesystem has no symlinks: %v", err)
	}

	if _, err := AddCodex(link, mnemo); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file, so the dotfiles repo no longer feeds the tool")
	}
	if !strings.Contains(slurp(t, real), "mnemo") {
		t.Error("the edit did not reach the file the symlink points at")
	}
	if mode := modeOf(t, real); mode != 0o600 {
		t.Errorf("the file is now %o, want 600: it can carry provider keys", mode)
	}
	// The copy sits next to the path the person gave, which is where they would
	// look for it, not next to the file the link happens to point at.
	if mode := modeOf(t, link+backupSuffix); mode != 0o600 {
		t.Errorf("the backup is %o, want 600: it is a copy of a file that can carry keys", mode)
	}
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return info.Mode().Perm()
}

// An .mcp.json is usually committed, so an entry in it is often a teammate's,
// with their own store and their own flags.
func TestTheClaudeFileWillNotTakeOverSomebodyElsesEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")
	original := "{\n  \"mcpServers\": {\n    \"mnemo\": {\n      \"command\": \"/opt/team/bin/mnemo-wrapper\",\n" +
		"      \"args\": [\"serve\", \"--store\", \"/srv/team-memory\"]\n    },\n" +
		"    \"postgres\": { \"command\": \"pg-mcp\" }\n  }\n}\n"
	put(t, path, original)

	result, err := AddClaudeFile(path, mnemo)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Refused {
		t.Fatalf("result = %+v, want a refusal", result)
	}
	if !strings.Contains(result.Advice, "teammate") {
		t.Errorf("the advice reads:\n%s", result.Advice)
	}
	if slurp(t, path) != original {
		t.Error("the file was changed anyway")
	}

	removed, err := RemoveClaudeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Outcome != Nothing {
		t.Errorf("removing = %+v", removed)
	}
	if slurp(t, path) != original {
		t.Error("removing deleted somebody else's entry")
	}
}

// An opencode entry with its own flags belongs to whoever wrote it, whatever the
// command happens to be called.
func TestOpencodeWillNotClaimAnEntryWithItsOwnFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	original := "{\"mcp\":{\"mnemo\":{\"type\":\"local\"," +
		"\"command\":[\"/opt/custom/mnemo\",\"serve\",\"--store\",\"/data/brain\"],\"enabled\":false}}}"
	put(t, path, original)

	result, err := AddOpencode(path, mnemo)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Refused {
		t.Fatalf("result = %+v", result)
	}
	if slurp(t, path) != original {
		t.Error("an entry with its own flags was overwritten")
	}

	removed, err := RemoveOpencode(path)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Outcome != Nothing {
		t.Errorf("removing = %+v", removed)
	}
	if slurp(t, path) != original {
		t.Error("an entry with its own flags was deleted")
	}
}

// A marketplace is a name, and a name can serve a different implementation than
// the one running here. Installing that and reporting success hands a chat a
// mnemo that keeps its memory somewhere else, and nothing downstream notices.
func TestClaudeRefusesAPluginThatIsNotThisBinarys(t *testing.T) {
	if !Installed("claude") {
		t.Skip("claude is not on this machine")
	}
	result, err := AddClaude("0.5.0", func(args ...string) (string, error) {
		if args[1] == "list" {
			return "mnemo@mnemo  0.4.0  enabled", nil
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Refused {
		t.Fatalf("result = %+v, want a refusal: the marketplace served a different implementation", result)
	}
	for _, want := range []string{"0.4.0", "does not read the store", "claude plugin uninstall", "--path"} {
		if !strings.Contains(result.Advice, want) {
			t.Errorf("the advice does not carry %q:\n%s", want, result.Advice)
		}
	}

	// A build from a working copy cannot compare versions, and must not refuse
	// on that account.
	dev, err := AddClaude("dev", func(args ...string) (string, error) {
		if args[1] == "list" {
			return "mnemo@mnemo  0.4.0  enabled", nil
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if dev.Outcome != Added {
		t.Errorf("a dev build = %+v", dev)
	}
}

// With no home directory mnemo does not know where a tool keeps its config, and
// an empty base joined to a relative path lands in whatever directory the
// command was run from.
func TestWithNoHomeThereIsNoToolConfigPath(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("CODEX_HOME", "")
	if got := CodexFile(); got != "" {
		t.Errorf("CodexFile is %q, want nothing rather than a relative path", got)
	}
	if got := OpencodeFile(); got != "" {
		t.Errorf("OpencodeFile is %q, want nothing rather than a relative path", got)
	}
}

// Marker lines inside a multi-line string are whole lines and mean nothing.
// Matching them would quietly rewrite the value the person wrote, so a block only
// counts as mnemo's when it actually contains mnemo's entry.
func TestMarkersInsideAStringAreNotABlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "model = \"a-model\"\n" +
		"note = \"\"\"\n" + codexBegin + "\n" + codexEnd + "\n\"\"\"\n" +
		"approval_policy = \"on-request\"\n"
	put(t, path, original)

	removed, err := RemoveCodex(path)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Outcome != Nothing {
		t.Errorf("removing = %+v, want nothing to remove", removed)
	}
	if slurp(t, path) != original {
		t.Errorf("removing rewrote the person's own value:\n%s", slurp(t, path))
	}

	// Adding still works, and leaves the string alone.
	if _, err := AddCodex(path, mnemo); err != nil {
		t.Fatal(err)
	}
	after := slurp(t, path)
	if !strings.Contains(after, "note = \"\"\"\n"+codexBegin) {
		t.Errorf("the string value was damaged:\n%s", after)
	}
	var parsed map[string]any
	if _, err := toml.Decode(after, &parsed); err != nil {
		t.Errorf("the edit left invalid TOML: %v\n%s", err, after)
	}
}
