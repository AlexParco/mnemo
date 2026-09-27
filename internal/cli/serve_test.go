package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

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

// Every tool the spec's memory half defines is actually served by the binary.
// The rule texts an agent reads name these by hand, so one that is specced and
// missing is an instruction to call something that is not there.
func TestTheBinaryServesEveryMemoryTool(t *testing.T) {
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
	} {
		if !served[want] {
			t.Errorf("%s is in the spec and not served", want)
		}
	}
	if len(listed.Tools) != 17 {
		var names []string
		for name := range served {
			names = append(names, name)
		}
		t.Errorf("%d tools served, want the 17 of the memory half: %q", len(listed.Tools), names)
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
