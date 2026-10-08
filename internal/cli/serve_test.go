package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/mailbox"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// binary is the built mnemo, compiled once for the whole package.
//
// Everything below runs against it over a pipe, the way a tool starts it.
// Calling Execute in-process would skip the part most likely to be wrong:
// whether anything other than the protocol ends up on stdout.
var binary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "mnemo-binary-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "mnemo")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	build := exec.Command("go", "build", "-o", path, "github.com/AlexParco/mnemo/cmd/mnemo")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return "", err
	}
	return path, nil
})

// built is the binary's path, or a fatal error.
func built(t *testing.T) string {
	t.Helper()
	path, err := binary()
	if err != nil {
		t.Fatalf("building mnemo: %v", err)
	}
	return path
}

// TestMain removes the binary once every test has finished with it.
func TestMain(m *testing.M) {
	code := m.Run()
	if path, err := binary(); err == nil {
		os.RemoveAll(filepath.Dir(path))
	}
	os.Exit(code)
}

// machine is a home directory with a store in it, and the environment that
// points mnemo at both. Nothing here touches the developer's own store.
func machine(t *testing.T, withStore bool) (home string, env []string) {
	t.Helper()
	home = t.TempDir()
	store := filepath.Join(home, "store")
	if withStore {
		for _, sub := range []string{filepath.Join("projects", "shop"), "memories", "shared"} {
			if err := os.MkdirAll(filepath.Join(store, sub), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		write(t, filepath.Join(store, "projects", "shop", "INDEX.md"),
			"---\nslug: shop\nname: Shop\nstatus: active\nupdated: 2026-01-02\n---\n\nThe shop.\n")
		write(t, filepath.Join(store, "projects", "shop", "pending.md"),
			"## In progress\n\n- [ ] wire the parser\n\n## Done\n\n- [x] chose the format\n")
		write(t, filepath.Join(store, "memories", "a-decision.md"),
			"---\nid: a-decision\ntype: decision\nprojects: [shop]\nupdated: 2026-01-02\n---\n\nWe chose plain text.\n")
	}
	return home, []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"PATH=" + os.Getenv("PATH"),
		"MNEMO_DIR=" + store,
		"MNEMO_MACHINE=testbox",
		"XDG_STATE_HOME=" + filepath.Join(home, "state"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// complaints collects what the process writes to stderr.
//
// os/exec copies it on a goroutine of its own, which keeps writing while the
// test reads, so the buffer needs a lock. The race detector found this one, and
// it would have been an occasional unexplained failure in CI.
type complaints struct {
	mutex sync.Mutex
	seen  bytes.Buffer
}

func (c *complaints) Write(p []byte) (int, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.seen.Write(p)
}

func (c *complaints) String() string {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.seen.String()
}

func (c *complaints) empty() bool { return c.String() == "" }

// serve starts the binary and connects a real MCP client to it.
func serve(t *testing.T, env []string) (*mcp.ClientSession, *complaints) {
	t.Helper()
	command := exec.Command(built(t), "serve")
	command.Env = env
	// stderr is where mnemo is allowed to speak. Keeping it lets a failure say
	// what the process complained about instead of only that it went away.
	said := &complaints{}
	command.Stderr = said

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("connecting to the binary: %v\nstderr:\n%s", err, said)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, said
}

func TestTheBinaryServesTheMemoryTools(t *testing.T) {
	_, env := machine(t, true)
	session, complaints := serve(t, env)

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("listing tools: %v\nstderr:\n%s", err, complaints)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{
		"mnemo_status", "mnemo_list_projects", "mnemo_load_project",
		"mnemo_search_memories", "mnemo_read_memory", "mnemo_guide",
	} {
		if !names[want] {
			t.Errorf("the binary does not offer %s", want)
		}
	}

	result, err := session.CallTool(t.Context(),
		&mcp.CallToolParams{Name: "mnemo_load_project", Arguments: map[string]any{"slug": "shop"}})
	if err != nil {
		t.Fatalf("loading: %v\nstderr:\n%s", err, complaints)
	}
	if result.IsError {
		t.Fatalf("the binary refused: %v", result.Content)
	}
	first, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("the first block is %T", result.Content[0])
	}
	if !strings.HasPrefix(first.Text, "shop — Shop") {
		t.Errorf("the first block is not the card:\n%s", first.Text)
	}
	// The card carries what is left, never what is finished.
	if !strings.Contains(first.Text, "wire the parser") {
		t.Errorf("the card is missing the open item:\n%s", first.Text)
	}
	if strings.Contains(first.Text, "chose the format") {
		t.Errorf("the card shows finished work:\n%s", first.Text)
	}

	if !complaints.empty() {
		t.Errorf("the binary complained on a clean run:\n%s", complaints)
	}
}

// The machine label reaches the answer, which is what decides whether a pending
// item stamped for a machine is this one's to act on.
func TestTheBinaryKnowsWhichMachineItIs(t *testing.T) {
	_, env := machine(t, true)
	session, complaints := serve(t, env)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mnemo_status"})
	if err != nil {
		t.Fatalf("status: %v\nstderr:\n%s", err, complaints)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "machine: testbox") {
		t.Errorf("status does not name the machine:\n%s", text)
	}
	if !strings.Contains(text, "projects: 1 · memories: 1") {
		t.Errorf("status does not count the store:\n%s", text)
	}
}

// A machine that has never saved gets an answer that says so. Starting the
// server must not bring a store into being: a store created by reading would
// sync an empty memory over the real one.
func TestServingDoesNotCreateAStore(t *testing.T) {
	home, env := machine(t, false)
	session, complaints := serve(t, env)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mnemo_list_projects"})
	if err != nil {
		t.Fatalf("listing: %v\nstderr:\n%s", err, complaints)
	}
	if !result.IsError {
		t.Fatal("an absent store was reported as an empty one")
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "There is no mnemo store yet") {
		t.Errorf("the refusal reads:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(home, "store")); !os.IsNotExist(err) {
		t.Error("serving created a store")
	}
}

// stdout is the protocol. Anything else written there corrupts the stream, and
// the failure looks like a broken client rather than a stray line.
func TestNothingButTheProtocolReachesStdout(t *testing.T) {
	_, env := machine(t, true)
	// A setting mnemo cannot honour, which it has to report. The report must go
	// to stderr; on stdout it would be a parse error in the client.
	env = append(env, "MNEMO_LANG=fr")

	session, complaints := serve(t, env)
	result, err := session.CallTool(t.Context(),
		&mcp.CallToolParams{Name: "mnemo_load_project", Arguments: map[string]any{"slug": "shop"}})
	if err != nil {
		t.Fatalf("the stream did not survive a warning: %v\nstderr:\n%s", err, complaints)
	}
	if result.IsError {
		t.Fatalf("refused: %v", result.Content)
	}
	if !strings.Contains(complaints.String(), "MNEMO_LANG") {
		t.Errorf("the ignored setting was not reported on stderr:\n%s", complaints)
	}
}

func TestVersion(t *testing.T) {
	_, env := machine(t, false)
	command := exec.Command(built(t), "version")
	command.Env = env
	out, err := command.Output()
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if strings.TrimSpace(string(out)) != Version {
		t.Errorf("the binary reports %q, the package says %q", strings.TrimSpace(string(out)), Version)
	}
}

// A whole save, end to end, through the binary: create a project, write a fact,
// replace the pending list, commit. What matters is that the files on disk are
// what a later session will read, so they are read back from disk and not from
// what the tools said they did.
func TestTheBinaryCanSaveASession(t *testing.T) {
	home, env := machine(t, false)
	// Commits need an identity, and the test must not borrow the developer's.
	gitconfig := filepath.Join(home, "gitconfig")
	write(t, gitconfig, "[user]\n\tname = Test\n\temail = test@example.invalid\n")
	env = append(env, "GIT_CONFIG_GLOBAL="+gitconfig, "GIT_CONFIG_NOSYSTEM=1")

	session, complaints := serve(t, env)
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v\nstderr:\n%s", name, err, complaints)
		}
		if result.IsError {
			t.Fatalf("%s refused: %v\nstderr:\n%s", name, result.Content[0], complaints)
		}
		return result
	}

	call("mnemo_upsert_project", map[string]any{"slug": "shop", "name": "Shop"})
	call("mnemo_write_memory", map[string]any{
		"id": "plain-text-wins", "projects": []any{"shop"}, "type": "decision",
		"body": "We chose plain text over a database.",
	})
	call("mnemo_write_pending", map[string]any{
		"slug":    "shop",
		"content": "## In progress\n\n- [ ] wire the parser\n\n## Done\n\n- [x] chose the format\n",
	})
	committed := call("mnemo_commit", map[string]any{"message": "save(shop): the first session"})

	store := filepath.Join(home, "store")
	for _, rel := range []string{
		filepath.Join("projects", "shop", "INDEX.md"),
		filepath.Join("projects", "shop", "pending.md"),
		filepath.Join("memories", "plain-text-wins.md"),
		filepath.Join("shared", "SCHEMA.md"),
		".git",
	} {
		if _, err := os.Stat(filepath.Join(store, rel)); err != nil {
			t.Errorf("%s is missing from the store", rel)
		}
	}
	if text := committed.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "Committed ") {
		t.Errorf("the commit reported:\n%s", text)
	}

	// The next session reads what this one wrote: the card carries the open
	// item and not the finished one.
	loaded := call("mnemo_load_project", map[string]any{"slug": "shop"})
	card := loaded.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(card, "wire the parser") {
		t.Errorf("the card lost the open item:\n%s", card)
	}
	if strings.Contains(card, "chose the format") {
		t.Errorf("the card shows finished work:\n%s", card)
	}
	if !strings.Contains(strings.Join(allText(loaded), "\n"), "plain-text-wins") {
		t.Error("the memory written this session is not in the load")
	}

	if complaints.String() != "" {
		t.Errorf("the binary complained during a clean save:\n%s", complaints)
	}
}

func allText(result *mcp.CallToolResult) []string {
	var out []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			out = append(out, text.Text)
		}
	}
	return out
}

// A machine with no home directory has nowhere to keep a store. Serving anyway
// would make every path relative, so the store would be created inside the
// directory the tool started mnemo in — the user's own repository — and with a
// hub configured the whole of their memory would be cloned into it.
func TestWithNowhereToPutTheStoreItRefusesToServe(t *testing.T) {
	work := t.TempDir()
	state := t.TempDir()

	command := exec.Command(built(t), "serve")
	command.Dir = work
	// No HOME, no USERPROFILE, no MNEMO_DIR, no XDG_DATA_HOME: nothing that says
	// where a store belongs. XDG_STATE_HOME is set, as it often is, which is
	// what makes the locks resolve while the store does not.
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "XDG_STATE_HOME=" + state}
	var said complaints
	command.Stderr = &said
	output, err := command.Output()

	if err == nil {
		t.Error("mnemo served with nowhere to put the store")
	}
	if !strings.Contains(said.String(), "nowhere to keep the store") {
		t.Errorf("it did not say why:\nstderr: %s\nstdout: %s", said.String(), output)
	}

	entries, readErr := os.ReadDir(work)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("it wrote into the working directory: %q", names)
	}
}

// The tool that replaces pending.md is told to send back a merged version of
// the file. So the load has to carry the file, not only mnemo's parse of it:
// the parse keeps headings and checkbox lines, drops prose and cuts each item
// at a hundred characters, and an agent rebuilding from it deletes the rest.
func TestALoadCarriesThePendingFileItself(t *testing.T) {
	home, env := machine(t, true)
	long := strings.Repeat("a long task description ", 8)
	write(t, filepath.Join(home, "store", "projects", "shop", "pending.md"),
		"## In progress\n\nProse that explains the section.\n\n- [ ] "+long+"\n")

	session, complaints := serve(t, env)
	result, err := session.CallTool(t.Context(),
		&mcp.CallToolParams{Name: "mnemo_load_project", Arguments: map[string]any{"slug": "shop"}})
	if err != nil {
		t.Fatalf("loading: %v\nstderr:\n%s", err, complaints)
	}
	detail := allText(result)[1]

	if !strings.Contains(detail, "pending_file") {
		t.Fatalf("the load does not carry the file:\n%s", detail)
	}
	if !strings.Contains(detail, "Prose that explains the section.") {
		t.Error("the prose of pending.md is not in the answer, so a rewrite would delete it")
	}
	if !strings.Contains(detail, strings.TrimSpace(long)) {
		t.Error("the full item text is not in the answer, so a rewrite would truncate it")
	}
}

// Every tool the spec defines is actually served by the binary. The rule texts
// an agent reads name these by hand, so one that is specced and missing is an
// instruction to call something that is not there.
func TestTheBinaryServesEveryToolTheSpecDefines(t *testing.T) {
	_, env := machine(t, true)
	session, complaints := serve(t, env)

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("listing tools: %v\nstderr:\n%s", err, complaints)
	}
	served := map[string]bool{}
	for _, tool := range listed.Tools {
		served[tool.Name] = true
	}

	for _, want := range []string{
		"mnemo_status", "mnemo_list_projects", "mnemo_load_project", "mnemo_search_memories",
		"mnemo_read_memory", "mnemo_bootstrap", "mnemo_upsert_project", "mnemo_write_memory",
		"mnemo_write_pending", "mnemo_commit", "mnemo_sync", "mnemo_resolve_conflict",
		"mnemo_rebase", "mnemo_push", "mnemo_rename", "mnemo_forget", "mnemo_guide",
		"mailbox_peers", "mailbox_send", "mailbox_reply", "mailbox_read", "mailbox_wait",
	} {
		if !served[want] {
			t.Errorf("%s is in the spec and not served", want)
		}
	}
	if len(listed.Tools) != 22 {
		var names []string
		for name := range served {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Errorf("%d tools served, want the 22 of the spec: %q", len(listed.Tools), names)
	}
}

// `mnemo config` answers one question: why is my memory where it is. So every
// line has to say where the value came from, and the token must never appear.
func TestConfigSaysWhereEveryValueCameFrom(t *testing.T) {
	home, env := machine(t, false)
	configFile := filepath.Join(home, "config", "mnemo", "config.toml")
	write(t, configFile, "lang = \"es\"\n\n[store]\nremote = \"user@hub:mnemo.git\"\n\n[server]\nport = 7433\ntoken = \"not-a-real-token\"\n")

	run := func(args ...string) string {
		t.Helper()
		command := exec.Command(built(t), args...)
		command.Env = env
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("mnemo %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	out := run("config")
	for _, want := range []string{
		"machine", "testbox", "environment (MNEMO_MACHINE)",
		"lang", "es", "config file",
		"store.remote", "user@hub:mnemo.git",
		"store.autopush", "false", "default",
		"config file", configFile,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not-a-real-token") {
		t.Errorf("the token was printed:\n%s", out)
	}
	if !strings.Contains(out, "server.token") || !strings.Contains(out, "set") {
		t.Errorf("the token is not reported as set:\n%s", out)
	}

	// Setting says the change does not reach a chat that is already open, which
	// is the surprise that otherwise arrives much later.
	setting := run("config", "set", "machine", "Other Box")
	if !strings.Contains(setting, "already open keeps the old value") {
		t.Errorf("set does not warn about open chats:\n%s", setting)
	}
	if body := read(t, configFile); !strings.Contains(body, "other-box") {
		t.Errorf("the label was not stored normalised:\n%s", body)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(body)
}

// Registering is editing a file somebody else owns, so the binary has to leave
// everything it did not write exactly as it was.
func TestTheBinaryRegistersItselfWithoutDamagingTheFile(t *testing.T) {
	_, env := machine(t, false)
	work := t.TempDir()
	codex := filepath.Join(work, "codex", "config.toml")
	write(t, codex, "# keep me\nmodel = \"a-model\"\n")

	command := exec.Command(built(t), "mcp", "add", "codex")
	command.Env = append(env, "CODEX_HOME="+filepath.Join(work, "codex"))
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("mcp add codex: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "added") {
		t.Errorf("the output reads:\n%s", out)
	}

	after := read(t, codex)
	if !strings.Contains(after, "# keep me") {
		t.Errorf("the comment was lost:\n%s", after)
	}
	if !strings.Contains(after, "[mcp_servers.mnemo]") {
		t.Errorf("the entry is not there:\n%s", after)
	}
	// The entry points at a real mnemo, absolutely: a tool started from a
	// desktop launcher may not have ~/.local/bin on its PATH.
	if !strings.Contains(after, built(t)) {
		t.Errorf("the entry does not point at this binary:\n%s", after)
	}
}

// run is the binary, with its output and its exit code.
func runMnemo(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	command := exec.Command(built(t), args...)
	command.Env = env
	out, err := command.CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("mnemo %v: %v", args, err)
	}
	return string(out), code
}

// A script has to be able to tell "you typed it wrong" from "it did not work".
func TestUsageErrorsHaveTheirOwnExitCode(t *testing.T) {
	_, env := machine(t, false)

	for _, args := range [][]string{
		{"mcp"},
		{"mcp", "add", "gemini"},
		{"mcp", "add", "codex", "opencode"},
		{"config", "foo"},
		{"config", "set", "store.dir"},
		{"--nosuchflag"},
	} {
		out, code := runMnemo(t, env, args...)
		if code != 2 {
			t.Errorf("mnemo %v exited %d, want 2:\n%s", args, code, out)
		}
	}

	// A refusal is not a usage error: the command was right and mnemo would not
	// do it.
	work := t.TempDir()
	write(t, filepath.Join(work, "config.toml"), "[mcp_servers.mnemo]\ncommand = \"/mine\"\n")
	out, code := runMnemo(t, env, "mcp", "add", "codex", "--path", filepath.Join(work, "config.toml"))
	if code != 1 {
		t.Errorf("a refusal exited %d, want 1:\n%s", code, out)
	}

	if out, code := runMnemo(t, env, "config"); code != 0 {
		t.Errorf("config exited %d:\n%s", code, out)
	}
}

// Without this the only way to find out whether registering worked was to open a
// chat.
func TestStatusSaysWhatIsRegisteredAndWhereTheStoreIs(t *testing.T) {
	home, env := machine(t, true)

	out, code := runMnemo(t, env, "mcp", "status")
	if code != 0 {
		t.Fatalf("status exited %d:\n%s", code, out)
	}
	for _, want := range []string{"claude", "codex", "opencode", "store", filepath.Join(home, "store"), "hub"} {
		if !strings.Contains(out, want) {
			t.Errorf("status does not mention %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "exists") {
		t.Errorf("status does not say the store is there:\n%s", out)
	}

	// A machine that has never saved is told so, rather than left guessing.
	_, fresh := machine(t, false)
	out, _ = runMnemo(t, fresh, "mcp", "status")
	if !strings.Contains(out, "not created yet") {
		t.Errorf("status on a fresh machine:\n%s", out)
	}
	if !strings.Contains(out, "hub        none") {
		t.Errorf("status does not say nothing leaves this machine:\n%s", out)
	}
}

// Three lines saying nothing happened, and nothing about why or what to do, is
// how somebody spends an evening wondering where their memory is.
func TestSkippingSaysWhyAndWhatToDo(t *testing.T) {
	_, env := machine(t, false)
	// A PATH with none of the three tools on it.
	trimmed := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "PATH=") {
			trimmed = append(trimmed, entry)
		}
	}
	trimmed = append(trimmed, "PATH=/nonexistent")

	out, code := runMnemo(t, trimmed, "mcp", "add")
	if code != 0 {
		t.Errorf("nothing to do exited %d:\n%s", code, out)
	}
	for _, want := range []string{
		"claude is not on the PATH", "codex is not on the PATH",
		"None of the three tools was found", "mnemo mcp add codex",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not say %q:\n%s", want, out)
		}
	}
}

// With no home directory mnemo does not know where a tool keeps its config, and
// an entry written relative to the current directory lands inside whatever
// repository the command was run from, where the tool never looks.
func TestWithNoHomeItRefusesRatherThanWriteIntoTheCurrentDirectory(t *testing.T) {
	work := t.TempDir()
	command := exec.Command(built(t), "mcp", "add", "codex")
	command.Dir = work
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, _ := command.CombinedOutput()

	if !strings.Contains(string(out), "no home directory") {
		t.Errorf("the output reads:\n%s", out)
	}
	if !strings.Contains(string(out), "--path") {
		t.Errorf("the output does not offer a way through:\n%s", out)
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("it wrote into the working directory: %v", entries)
	}
}

// Saying a key was removed when it was never there makes a person believe they
// changed something they did not.
func TestUnsettingSomethingThatWasNeverSet(t *testing.T) {
	home, env := machine(t, false)
	configFile := filepath.Join(home, "config", "mnemo", "config.toml")
	write(t, configFile, "lang = \"es\"\n")

	out, code := runMnemo(t, env, "config", "unset", "store.dir")
	if code != 0 {
		t.Fatalf("exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "was not set") || !strings.Contains(out, "nothing changed") {
		t.Errorf("the output reads:\n%s", out)
	}

	// And one that was there really goes.
	out, _ = runMnemo(t, env, "config", "unset", "lang")
	if !strings.Contains(out, "Removed lang") {
		t.Errorf("the output reads:\n%s", out)
	}
	if body := read(t, configFile); strings.Contains(body, "lang") {
		t.Errorf("lang survived:\n%s", body)
	}
}

// A machine label that normalises to nothing stops every command that needs one,
// so printing it as a dash with no warning leaves the cause invisible.
func TestNoUsableMachineLabelIsSaidOutLoud(t *testing.T) {
	_, env := machine(t, false)
	trimmed := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "MNEMO_MACHINE=") {
			trimmed = append(trimmed, entry)
		}
	}
	trimmed = append(trimmed, "MNEMO_MACHINE=---")

	out, code := runMnemo(t, trimmed, "config")
	if code != 0 {
		t.Fatalf("exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "no usable machine label") {
		t.Errorf("nothing warns about it:\n%s", out)
	}
	if !strings.Contains(out, "mnemo config set machine") {
		t.Errorf("the warning does not say what to type:\n%s", out)
	}
}

// The watcher is the only way in that needs no tool call at all, so it is the
// only thing that reaches a chat doing nothing. Everything about it is "look,
// never touch".
func TestWatchAnnouncesWithoutTouchingAnything(t *testing.T) {
	home, env := machine(t, true)
	resolved := watchSettings(t, home, "boss")
	box := mailboxFor(resolved)

	// Another agent claims a name and leaves two messages.
	sender := mailbox.Caller{Name: "worker", Machine: resolved.Machine, Tool: "codex",
		Instance: mailbox.Instance(), Via: "local", PID: os.Getpid()}
	if _, err := box.Claim(t.Context(), sender); err != nil {
		t.Fatal(err)
	}
	receiver := mailbox.Caller{Name: "boss", Machine: resolved.Machine, Tool: "claude",
		Instance: mailbox.Instance(), Via: "local", PID: os.Getpid()}
	if _, err := box.Claim(t.Context(), receiver); err != nil {
		t.Fatal(err)
	}
	first, err := box.Send(t.Context(), sender, "boss@"+resolved.Machine, "the parser is done", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Send(t.Context(), sender, "boss@"+resolved.Machine,
		"and   the\n\ttests   pass", ""); err != nil {
		t.Fatal(err)
	}

	var out, errOut strings.Builder
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err := watch(ctx, resolved, "claude", &out, &errOut, 20*time.Millisecond); err != nil {
		t.Fatalf("watching: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("announced %d lines, want the two waiting:\n%s", len(lines), out.String())
	}
	// What is already waiting is announced when it starts, not only what arrives
	// afterwards: a chat that was closed when the message came still hears it.
	want := fmt.Sprintf("[mnemo] message %s from worker@%s: %q Read it with mailbox_read.",
		first.Message.ID, resolved.Machine, "the parser is done")
	if lines[0] != want {
		t.Errorf("the line is:\n%s\nwant:\n%s", lines[0], want)
	}
	// Runs of whitespace collapse: a notification is one line by definition.
	if !strings.Contains(lines[1], `"and the tests pass"`) {
		t.Errorf("the body was not flattened: %s", lines[1])
	}
	// Each message once per run.
	if strings.Count(out.String(), first.Message.ID) != 1 {
		t.Errorf("a message was announced twice:\n%s", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("it complained on a clean run: %s", errOut.String())
	}

	// And it marked nothing: the chat still has both to read.
	unread, err := box.Read(t.Context(), receiver)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 2 {
		t.Errorf("announcing consumed the post: %d left to read", len(unread))
	}
	// Nor did it take the name: the holder is still the chat.
	if holder, live, err := box.HeldBy(t.Context(), "boss@"+resolved.Machine); err != nil {
		t.Fatal(err)
	} else if !live || holder.Instance != receiver.Instance {
		t.Errorf("the watcher disturbed the holder: live=%v holder=%+v", live, holder)
	}
	_ = env
}

// A chat opened without `mnemo run` has memory and no mailbox. Nothing is wrong
// with that, so the watcher says nothing rather than explaining it every two
// seconds for the life of the session.
func TestWatchWithNoNameSaysNothing(t *testing.T) {
	home, _ := machine(t, true)
	resolved := watchSettings(t, home, "")

	var out, errOut strings.Builder
	if err := watch(t.Context(), resolved, "claude", &out, &errOut, time.Millisecond); err != nil {
		t.Fatalf("watching: %v", err)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("it said something:\nout: %s\nerr: %s", out.String(), errOut.String())
	}
}

// A preview is read by eye, which is the one place in mnemo that is true, so it
// is cut by code points rather than bytes.
func TestAPreviewIsNeverCutMidCharacter(t *testing.T) {
	long := strings.Repeat("ñ", 400)
	got := preview(long)
	if runes := []rune(got); len(runes) != previewChars {
		t.Errorf("the preview is %d code points, want %d", len(runes), previewChars)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a cut preview does not say it was cut: %q", got[len(got)-6:])
	}
	if strings.ContainsRune(got, '�') {
		t.Error("the preview was cut mid-character")
	}
	if short := preview("  already   short\n"); short != "already short" {
		t.Errorf("a short body came back as %q", short)
	}
}

// watchSettings resolves the settings a watcher runs with, for one agent name.
func watchSettings(t *testing.T, home, agent string) config.Settings {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("MNEMO_DIR", filepath.Join(home, "store"))
	t.Setenv("MNEMO_MACHINE", "testbox")
	t.Setenv("MNEMO_AGENT", agent)
	t.Setenv("MNEMO_REMOTE", "")
	file, err := config.LoadFrom(config.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	return config.Resolve(file, config.Overrides{})
}

// The hook runs before every edit, in somebody's working session. Everything
// about it is shaped by never getting in the way, so that is what is tested
// first: it never fails, whatever it is handed.
func TestTheHookNeverFails(t *testing.T) {
	home, _ := machine(t, true)
	watchSettings(t, home, "")

	for name, stdin := range map[string]string{
		"nothing":                        "",
		"not JSON":                       "{{{",
		"no session id":                  `{"transcript_path":"/nowhere"}`,
		"a transcript that is not there": `{"session_id":"s1","transcript_path":"/nowhere/at/all"}`,
		"a huge body":                    `{"session_id":"s1","body":"` + strings.Repeat("x", 100000) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := suggestSave(strings.NewReader(stdin)); got != "" && !strings.HasPrefix(got, "mnemo: ") {
				t.Errorf("it said %q", got)
			}
		})
	}
}

// The edit signal is the one that is on by default, so it has to count, nudge at
// the threshold, and not nag between nudges.
func TestTheHookNudgesOnEditsAndThenWaits(t *testing.T) {
	home, _ := machine(t, true)
	watchSettings(t, home, "")
	t.Setenv("MNEMO_SAVE_EDITS", "3")
	input := `{"session_id":"a-session"}`

	for i := 1; i <= 2; i++ {
		if got := suggestSave(strings.NewReader(input)); got != "" {
			t.Fatalf("it nudged after %d edit(s): %q", i, got)
		}
	}
	got := suggestSave(strings.NewReader(input))
	if !strings.Contains(got, "3 unsaved edits") {
		t.Fatalf("the nudge at the threshold reads %q", got)
	}
	for _, want := range []string{"this session", "/mnemo:save-context", "lose progress"} {
		if !strings.Contains(got, want) {
			t.Errorf("the nudge does not say %q: %q", want, got)
		}
	}
	// And then it waits a whole threshold before the next one.
	for i := 4; i <= 5; i++ {
		if again := suggestSave(strings.NewReader(input)); again != "" {
			t.Fatalf("it nudged again at edit %d: %q", i, again)
		}
	}
	if again := suggestSave(strings.NewReader(input)); !strings.Contains(again, "6 unsaved edits") {
		t.Errorf("the second nudge reads %q", again)
	}

	// Zero turns it off entirely.
	t.Setenv("MNEMO_SAVE_EDITS", "0")
	for i := 0; i < 5; i++ {
		if off := suggestSave(strings.NewReader(`{"session_id":"off"}`)); off != "" {
			t.Fatalf("it nudged with the signal off: %q", off)
		}
	}
}

// A save resets the count, because the whole point is unsaved work. The store's
// head is read from the files in .git rather than by running git: this is on the
// path of every edit and a process there would be felt.
func TestASaveResetsTheCount(t *testing.T) {
	home, _ := machine(t, true)
	resolved := watchSettings(t, home, "")
	t.Setenv("MNEMO_SAVE_EDITS", "2")
	git := filepath.Join(resolved.Paths.Store, ".git")
	write(t, filepath.Join(git, "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(git, "refs", "heads", "main"), "1111111111111111111111111111111111111111\n")
	input := `{"session_id":"s"}`

	suggestSave(strings.NewReader(input))
	if got := suggestSave(strings.NewReader(input)); !strings.Contains(got, "2 unsaved edits") {
		t.Fatalf("the first nudge reads %q", got)
	}

	// A save: the head moves.
	write(t, filepath.Join(git, "refs", "heads", "main"), "2222222222222222222222222222222222222222\n")
	if got := suggestSave(strings.NewReader(input)); got != "" {
		t.Errorf("it nudged on the edit right after a save: %q", got)
	}
	if got := suggestSave(strings.NewReader(input)); !strings.Contains(got, "2 unsaved edits") {
		t.Errorf("the count did not restart from the save: %q", got)
	}

	// A packed ref is what a freshly cloned store has, and it has to be read too.
	if err := os.Remove(filepath.Join(git, "refs", "heads", "main")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(git, "packed-refs"),
		"# pack-refs with: peeled fully-peeled sorted\n3333333333333333333333333333333333333333 refs/heads/main\n")
	if head := storeHead(resolved.Paths.Store); head != "3333333333333333333333333333333333333333" {
		t.Errorf("a packed ref read as %q", head)
	}
}

// Spanish when the resolved language is Spanish. The nudge is read by a person.
func TestTheNudgeFollowsTheLanguage(t *testing.T) {
	home, _ := machine(t, true)
	watchSettings(t, home, "")
	t.Setenv("MNEMO_SAVE_EDITS", "1")
	t.Setenv("MNEMO_LANG", "es")

	got := suggestSave(strings.NewReader(`{"session_id":"es"}`))
	// One edit, and a threshold of one is reachable, so the singular has to
	// exist in both languages: "1 ediciones" is how a text stops being somebody's
	// own language and starts being a translation.
	for _, want := range []string{"1 edición sin guardar", "en esta sesión", "para no perder el avance"} {
		if !strings.Contains(got, want) {
			t.Errorf("the Spanish nudge does not say %q: %q", want, got)
		}
	}
	if strings.Contains(got, "unsaved") {
		t.Errorf("the Spanish nudge carries English: %q", got)
	}
	if plural := suggestSave(strings.NewReader(`{"session_id":"es"}`)); !strings.Contains(plural,
		"2 ediciones sin guardar") {
		t.Errorf("the Spanish plural reads %q", plural)
	}
}

// The context signal is off unless asked for, because it reads a transcript whose
// format belongs to the tool and can change under us.
func TestTheContextSignalIsOffByDefaultAndReadsTheLastUsage(t *testing.T) {
	home, _ := machine(t, true)
	watchSettings(t, home, "")
	transcript := filepath.Join(home, "transcript.jsonl")
	write(t, transcript, strings.Join([]string{
		`{"message":{"usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":0}}}`,
		`{"type":"something-else"}`,
		`{"message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":80000,"cache_creation_input_tokens":4000}}}`,
	}, "\n")+"\n")
	input := fmt.Sprintf(`{"session_id":"ctx","transcript_path":%q}`, transcript)

	t.Setenv("MNEMO_SAVE_EDITS", "0")
	if got := suggestSave(strings.NewReader(input)); got != "" {
		t.Errorf("the context signal nudged while off: %q", got)
	}

	// The three counts add up, and the last entry is the one that matters.
	if used := contextTokens(transcript); used != 85000 {
		t.Errorf("the context is %d tokens, want 85000 from the last entry", used)
	}

	t.Setenv("MNEMO_SAVE_TOKENS", "80000")
	got := suggestSave(strings.NewReader(input))
	if !strings.Contains(got, "context at ~85k tokens") {
		t.Errorf("the context nudge reads %q", got)
	}
	// And it does not nudge again until a whole step later.
	if again := suggestSave(strings.NewReader(input)); again != "" {
		t.Errorf("it nudged twice for the same context: %q", again)
	}
}

// The shape the hook protocol expects: the same sentence twice, once for the
// person and once for the model.
func TestTheNudgeIsPrintedInTheShapeTheHookExpects(t *testing.T) {
	var out strings.Builder
	printNudge(&out, "mnemo: 40 unsaved edits this session.")

	var parsed struct {
		SystemMessage      string `json:"systemMessage"`
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out.String()), &parsed); err != nil {
		t.Fatalf("what it printed is not JSON: %v\n%s", err, out.String())
	}
	if parsed.SystemMessage == "" || parsed.HookSpecificOutput.AdditionalContext != parsed.SystemMessage {
		t.Errorf("parsed = %+v; the person and the model get the same sentence", parsed)
	}
	if parsed.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("the event is %q", parsed.HookSpecificOutput.HookEventName)
	}
}

// The plugin ships three things that invoke the binary, and shipping one that
// invokes a command the binary does not have is worse than shipping without it.
func TestThePluginOnlyInvokesCommandsThatExist(t *testing.T) {
	root := filepath.Join("..", "..")
	var invoked []string

	var plugin struct {
		Version    string `json:"version"`
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(root, "plugin", ".claude-plugin", "plugin.json"))),
		&plugin); err != nil {
		t.Fatalf("plugin.json is not JSON: %v", err)
	}
	entry, ok := plugin.MCPServers["mnemo"]
	if !ok {
		t.Fatal("the plugin declares no mnemo server, so a chat gets no tools")
	}
	// By name, not by path: a plugin cannot know where the binary is.
	if entry.Command != "mnemo" {
		t.Errorf("the plugin runs %q; it has to run mnemo from the PATH", entry.Command)
	}
	if entry.Env["MNEMO_TOOL"] != "claude" {
		t.Errorf("the plugin does not say which tool it is: %v", entry.Env)
	}
	invoked = append(invoked, strings.Join(entry.Args, " "))

	var monitors []struct {
		Name    string `json:"name"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(root, "plugin", "monitors", "monitors.json"))),
		&monitors); err != nil {
		t.Fatalf("monitors.json is not JSON: %v", err)
	}
	if len(monitors) != 1 || monitors[0].Name != "mailbox" {
		t.Fatalf("monitors = %+v", monitors)
	}
	invoked = append(invoked, strings.TrimPrefix(monitors[0].Command, "mnemo "))

	var hooks struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(root, "plugin", "hooks", "hooks.json"))),
		&hooks); err != nil {
		t.Fatalf("hooks.json is not JSON: %v", err)
	}
	pre := hooks.Hooks["PreToolUse"]
	if len(pre) != 1 || pre[0].Matcher != "Edit|Write" || len(pre[0].Hooks) != 1 {
		t.Fatalf("the PreToolUse hook is %+v", pre)
	}
	invoked = append(invoked, strings.TrimPrefix(pre[0].Hooks[0].Command, "mnemo "))

	// Every one of them actually runs. --help exits 0 for a command that exists
	// and non-zero for one that does not, without doing anything.
	_, env := machine(t, false)
	for _, command := range invoked {
		args := append(strings.Fields(command), "--help")
		if out, code := runMnemo(t, env, args...); code != 0 {
			t.Errorf("the plugin invokes `mnemo %s`, which this binary does not have:\n%s", command, out)
		}
	}
}

// The hook is the third way in, and the only one that reaches a chat that is
// working hard: not idle, so no notification wakes it, and not calling mnemo, so
// no answer carries the line. Its next edit is.
func TestTheHookTellsABusyChatItHasPost(t *testing.T) {
	home, _ := machine(t, true)
	resolved := watchSettings(t, home, "boss")
	// High enough that nothing here crosses it: what is being tested is the post,
	// not the save nudge.
	t.Setenv("MNEMO_SAVE_EDITS", "500")
	box := mailboxFor(resolved)

	sender := mailbox.Caller{Name: "worker", Machine: resolved.Machine, Tool: "codex",
		Instance: mailbox.Instance(), Via: "local", PID: os.Getpid()}
	receiver := mailbox.Caller{Name: "boss", Machine: resolved.Machine, Tool: "claude",
		Instance: mailbox.Instance(), Via: "local", PID: os.Getpid()}
	for _, caller := range []mailbox.Caller{sender, receiver} {
		if _, err := box.Claim(t.Context(), caller); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := box.Send(t.Context(), sender, "boss@"+resolved.Machine, "the parser is done", ""); err != nil {
		t.Fatal(err)
	}

	input := `{"session_id":"busy"}`
	got := suggestSave(strings.NewReader(input))
	if !strings.Contains(got, "1 message(s) waiting for you, from worker@"+resolved.Machine) {
		t.Fatalf("the hook said %q", got)
	}
	if !strings.Contains(got, "mailbox_read") {
		t.Errorf("the hook does not say how to read it: %q", got)
	}

	// Once per message, not at every edit until it is read. A hook that repeated
	// itself would be the thing that gets in the way.
	if again := suggestSave(strings.NewReader(input)); again != "" {
		t.Errorf("the hook announced the same message twice: %q", again)
	}

	// And it marked nothing read: announcing is not delivering, and the chat
	// still has the message.
	unread, err := box.Read(t.Context(), receiver)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 1 {
		t.Errorf("the hook consumed the post: %d left to read", len(unread))
	}

	// A new message is announced, because it is new.
	if _, err := box.Send(t.Context(), sender, "boss@"+resolved.Machine, "deployed", ""); err != nil {
		t.Fatal(err)
	}
	if next := suggestSave(strings.NewReader(input)); !strings.Contains(next, "1 message(s) waiting") {
		t.Errorf("a message that arrived later was not announced: %q", next)
	}

	// Two senders read as two senders. The same sentence is said by the tool
	// answers and by this hook, and when each built its own list of names they
	// drifted — one said "a and b", the other "a, b".
	other := mailbox.Caller{Name: "deployer", Machine: resolved.Machine, Tool: "claude",
		Instance: mailbox.Instance(), Via: "local", PID: os.Getpid()}
	if _, err := box.Claim(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	for _, from := range []mailbox.Caller{sender, other} {
		if _, err := box.Send(t.Context(), from, "boss@"+resolved.Machine, "another one", ""); err != nil {
			t.Fatal(err)
		}
	}
	both := suggestSave(strings.NewReader(input))
	if !strings.Contains(both, fmt.Sprintf("2 message(s) waiting for you, from worker@%s and deployer@%s",
		resolved.Machine, resolved.Machine)) {
		t.Errorf("two senders read as %q", both)
	}
}

// A chat with no name has no mailbox, and the hook must not invent one: it runs
// before every edit in every session, including the ones that never asked for a
// mailbox at all.
func TestTheHookSaysNothingAboutPostToAChatWithNoName(t *testing.T) {
	home, _ := machine(t, true)
	watchSettings(t, home, "")
	t.Setenv("MNEMO_SAVE_EDITS", "500")

	if got := suggestSave(strings.NewReader(`{"session_id":"nameless"}`)); got != "" {
		t.Errorf("it said %q", got)
	}
}

// A failure must not end the watch, and must not be repeated every two seconds
// for as long as it lasts: a tunnel that drops for a minute is a normal day.
func TestWatchKeepsGoingAndComplainsOnlyOnce(t *testing.T) {
	home, _ := machine(t, true)
	resolved := watchSettings(t, home, "boss")
	// A state file that cannot be read is the shape every failure takes here:
	// peek reads two files, and this is the one it reads first.
	write(t, filepath.Join(resolved.Paths.Mailbox, "state.json"), "{not json")

	var out, errOut strings.Builder
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if err := watch(ctx, resolved, "claude", &out, &errOut, 20*time.Millisecond); err != nil {
		t.Fatalf("a failure ended the watch: %v", err)
	}

	said := strings.Split(strings.TrimSpace(errOut.String()), "\n")
	if len(said) != 1 {
		t.Errorf("it complained %d times over ten or more looks:\n%s", len(said), errOut.String())
	}
	if !strings.HasPrefix(said[0], "mnemo watch: ") {
		t.Errorf("the complaint reads %q", said[0])
	}
	// Nothing on stdout, because every line there becomes a notification.
	if out.Len() != 0 {
		t.Errorf("it announced something while failing: %s", out.String())
	}
}

// The hook keeps a file per session. Without a sweep they accumulate for as long
// as somebody uses mnemo, in a directory nobody ever looks at.
func TestOldSessionStateIsSweptUp(t *testing.T) {
	home, _ := machine(t, true)
	resolved := watchSettings(t, home, "")
	t.Setenv("MNEMO_SAVE_EDITS", "500")

	old := filepath.Join(resolved.Paths.Reminder, "long-gone.json")
	write(t, old, `{"edits":12}`)
	if err := os.Chtimes(old, time.Now().Add(-8*24*time.Hour), time.Now().Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	recent := filepath.Join(resolved.Paths.Reminder, "yesterday.json")
	write(t, recent, `{"edits":3}`)
	if err := os.Chtimes(recent, time.Now().Add(-24*time.Hour), time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	suggestSave(strings.NewReader(`{"session_id":"today"}`))

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("a session nobody will open again is still there: %v", err)
	}
	// And a session from yesterday is somebody's work in progress.
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("yesterday's session was swept up: %v", err)
	}
}
