package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/AlexParco/mnemo/internal/build"
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

// The version line names the version, where the build came from, and the api
// number — and `--version` says the same thing as `version`, because somebody
// who types one and gets a different answer from the other has to wonder which
// is true.
func TestVersion(t *testing.T) {
	_, env := machine(t, false)
	said := map[string]string{}
	for _, how := range []string{"version", "--version"} {
		command := exec.Command(built(t), how)
		command.Env = env
		out, err := command.Output()
		if err != nil {
			t.Fatalf("%s: %v", how, err)
		}
		said[how] = strings.TrimSpace(string(out))
	}
	if said["version"] != said["--version"] {
		t.Errorf("version says %q and --version says %q", said["version"], said["--version"])
	}
	// Built from a working copy, so it says so rather than naming a release it
	// is not. The shape is what a release prints, with the values it has.
	want := fmt.Sprintf("mnemo %s (commit %s, built %s, api %d)",
		build.Version, build.Commit, build.Date, build.API)
	if said["version"] != want {
		t.Errorf("the binary reports %q, want %q", said["version"], want)
	}
	if !strings.Contains(said["version"], "api ") {
		t.Error("the line does not carry the api number, which is what decides whether two machines can talk")
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
// runWait is how long a command is given before the test calls it hung.
//
// Every command here either does something and stops or refuses and stops, so a
// command still running after this is a defect — and a defect that, without a
// bound, takes the whole suite down with it rather than failing one test. A
// negative control that makes `serve --http` start instead of refusing did
// exactly that.
const runWait = 60 * time.Second

func runMnemo(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), runWait)
	defer cancel()
	command := exec.CommandContext(ctx, built(t), args...)
	command.Env = env
	out, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("mnemo %v was still running after %s, so it is not doing what it says:\n%s",
			args, runWait, out)
	}
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
	if len(unread.Messages) != 2 {
		t.Errorf("announcing consumed the post: %d left to read", len(unread.Messages))
	}
	// Nor did it take the name: the holder is still the chat.
	if holder, live, err := box.Occupied(t.Context(), "boss@"+resolved.Machine); err != nil {
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
	if len(unread.Messages) != 1 {
		t.Errorf("the hook consumed the post: %d left to read", len(unread.Messages))
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
	// Three, not two: the count is everything mailbox_read will hand over,
	// including the message announced a moment ago and still unread. Saying "2
	// waiting" and then delivering three is worse than saying nothing.
	if !strings.Contains(both, fmt.Sprintf("3 message(s) waiting for you, from worker@%s and deployer@%s",
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

// invoke calls a tool on a session, failing the test if the call itself does not
// get through. A refusal is an answer and comes back for the test to look at.
func invoke(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return result
}

// Two real chats, two processes, one message and the answer back. This is the
// test the whole mailbox exists to pass, and the only one that proves the parts
// meet: two `mnemo serve` processes that know nothing of each other, a name
// claimed in each, and files as the only thing between them.
func TestAMessageCrossesBetweenTwoRealChatsAndTheReplyComesBack(t *testing.T) {
	home, env := machine(t, true)
	mailboxDir := filepath.Join(home, "mailbox")
	chat := func(name, tool string) (*mcp.ClientSession, *complaints) {
		return serve(t, append(env,
			"MNEMO_MAILBOX_DIR="+mailboxDir, "MNEMO_AGENT="+name, "MNEMO_TOOL="+tool))
	}

	boss, bossSaid := chat("boss", "claude")
	worker, workerSaid := chat("worker-api", "codex")

	// Each process says on stderr that it holds its name, which is the only
	// place a person can see it: stdout carries the protocol.
	for name, said := range map[string]*complaints{"boss": bossSaid, "worker-api": workerSaid} {
		if want := "holding " + name + "@testbox"; !strings.Contains(said.String(), want) {
			t.Errorf("%s did not say it holds its name:\n%s", name, said)
		}
	}

	// A bare name resolves, because the other chat claimed it when it started —
	// which is the point of claiming at startup rather than at first use.
	sent := invoke(t, boss, "mailbox_send", map[string]any{
		"to": "worker-api", "body": "add the coupons endpoint", "project": "shop",
	})
	if sent.IsError {
		t.Fatalf("sending: %s\nstderr:\n%s", allText(sent), bossSaid)
	}
	first := allText(sent)[0]
	if !strings.Contains(first, "to worker-api@testbox.") {
		t.Errorf("the send says %q", first)
	}
	if strings.Contains(first, "warning") {
		t.Errorf("a message to a chat that is open warned about waiting: %q", first)
	}

	delivered := allText(invoke(t, worker, "mailbox_read", nil))
	if !strings.Contains(delivered[0], "from boss@testbox (claude)") ||
		!strings.Contains(delivered[0], "add the coupons endpoint") {
		t.Fatalf("what the worker read:\n%s", strings.Join(delivered, "\n--\n"))
	}
	// It names the project rather than carrying its context, and says to load it.
	if !strings.Contains(delivered[0], "mnemo_load_project") {
		t.Error("the project was not pointed at")
	}
	id := strings.TrimSuffix(strings.TrimPrefix(strings.Fields(delivered[0])[0], "["), "]")

	answered := invoke(t, worker, "mailbox_reply", map[string]any{"id": id, "body": "done, tests pass"})
	if answered.IsError {
		t.Fatalf("replying: %s\nstderr:\n%s", allText(answered), workerSaid)
	}

	// The answer goes back to exactly the chat that asked.
	back := allText(invoke(t, boss, "mailbox_read", nil))
	if !strings.Contains(back[0], "in reply to "+id) || !strings.Contains(back[0], "done, tests pass") {
		t.Fatalf("what came back:\n%s", strings.Join(back, "\n--\n"))
	}

	// And each sees the other as live, in the tool it really runs in.
	table := strings.Join(allText(invoke(t, boss, "mailbox_peers", nil)), "\n")
	for _, want := range []string{"boss@testbox (you)", "| claude | live |", "worker-api@testbox", "| codex | live |"} {
		if !strings.Contains(table, want) {
			t.Errorf("peers does not show %q:\n%s", want, table)
		}
	}

	// A third chat asking for a name that is open is turned away, and told where
	// to find the one that has it.
	third, thirdSaid := chat("worker-api", "opencode")
	if !strings.Contains(thirdSaid.String(), "worker-api@testbox is already open in codex") {
		t.Errorf("the third chat was not told who has the name:\n%s", thirdSaid)
	}
	// It still has its memory — losing the mailbox is not losing the memory —
	// and every mailbox tool but peers refuses with the same sentence.
	if result := invoke(t, third, "mnemo_list_projects", nil); result.IsError {
		t.Errorf("a chat that lost the name lost its memory too: %s", allText(result))
	}
	refused := invoke(t, third, "mailbox_read", nil)
	if !refused.IsError {
		t.Error("a chat that does not hold the name read its post")
	}
	if said := allText(refused)[0]; !strings.Contains(said, "already open in codex") {
		t.Errorf("it refuses with %q", said)
	}
	// Peers answers, with the warning on the identity line: it is how the person
	// finds out which window to close.
	seen := allText(invoke(t, third, "mailbox_peers", nil))
	if identity := seen[len(seen)-1]; !strings.Contains(identity, "warning: worker-api@testbox is already open") {
		t.Errorf("the identity line is %q", identity)
	}
}

// The watcher and the chat it watches start at the same moment, so the watcher
// can easily look before the name has been claimed. What it must not do then is
// record itself as having announced what was already waiting.
func TestWatchStartedBeforeTheChatAnnouncesWhatWasAlreadyThere(t *testing.T) {
	home, _ := machine(t, true)
	resolved := watchSettings(t, home, "boss")
	box := mailboxFor(resolved)

	// A message is left for a name nobody has claimed yet. A direct message
	// waits for an address that does not exist, which is the whole reason
	// somebody can be sent work before they open the chat.
	sender := mailbox.Caller{Name: "worker", Machine: resolved.Machine, Tool: "codex",
		Instance: mailbox.Instance(), Via: "local", PID: os.Getpid()}
	if _, err := box.Claim(t.Context(), sender); err != nil {
		t.Fatal(err)
	}
	waiting, err := box.Send(t.Context(), sender, "boss@"+resolved.Machine, "start with the parser", "")
	if err != nil {
		t.Fatal(err)
	}

	// One watcher, running across the moment the chat opens — which is the
	// real shape: the monitor is started once and lives as long as the session.
	var out, errOut strings.Builder
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watch(ctx, resolved, "claude", &out, &errOut, 20*time.Millisecond) }()

	// It looks a few times while the address is still unknown, which is what it
	// does for as long as the chat takes to start, and then the chat claims it.
	time.Sleep(100 * time.Millisecond)
	receiver := mailbox.Caller{Name: "boss", Machine: resolved.Machine, Tool: "claude",
		Instance: mailbox.Instance(), Via: "local", PID: os.Getpid()}
	if _, err := box.Claim(t.Context(), receiver); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("watching: %v", err)
	}

	// What was waiting all along is announced. Before this, the first look while
	// the address was unknown moved the watcher past it, and the chat sat idle
	// next to post nobody ever mentioned.
	if !strings.Contains(out.String(), waiting.Message.ID) {
		t.Errorf("what was waiting was never announced:\n%s", out.String())
	}
	if strings.Count(out.String(), waiting.Message.ID) != 1 {
		t.Errorf("it was announced more than once:\n%s", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("it complained: %s", errOut.String())
	}
}

// The log is shared by every agent on the machine, so the hook must measure what
// it has announced by this chat's own post. Two other agents talking to each
// other would otherwise make it repeat itself at every edit.
func TestTheHookIsNotRetriggeredByOtherAgentsTraffic(t *testing.T) {
	home, _ := machine(t, true)
	resolved := watchSettings(t, home, "boss")
	t.Setenv("MNEMO_SAVE_EDITS", "500")
	box := mailboxFor(resolved)

	caller := func(name string) mailbox.Caller {
		return mailbox.Caller{Name: name, Machine: resolved.Machine, Tool: "claude",
			Instance: mailbox.Instance(), Via: "local", PID: os.Getpid()}
	}
	boss, worker, other := caller("boss"), caller("worker"), caller("other")
	for _, who := range []mailbox.Caller{boss, worker, other} {
		if _, err := box.Claim(t.Context(), who); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := box.Send(t.Context(), worker, "boss@"+resolved.Machine, "for you", ""); err != nil {
		t.Fatal(err)
	}

	input := `{"session_id":"busy"}`
	if got := suggestSave(strings.NewReader(input)); !strings.Contains(got, "1 message(s) waiting") {
		t.Fatalf("the hook said %q", got)
	}

	// Two other agents talk to each other. Nothing new for this chat.
	for i := 0; i < 3; i++ {
		if _, err := box.Send(t.Context(), worker, "other@"+resolved.Machine, "not for you", ""); err != nil {
			t.Fatal(err)
		}
	}
	if again := suggestSave(strings.NewReader(input)); again != "" {
		t.Errorf("somebody else's traffic made it repeat itself: %q", again)
	}
}

// `mnemo run` is the command everything else starts from, and it had no test.
//
// What it promises is that everything which can be checked is checked before the
// tool opens, because a chat that opens and turns out to have no memory or no
// mailbox is a chat nobody finds out about until they need one. So each refusal
// is checked in order, and then the tool is really opened.
func TestRunChecksEverythingBeforeOpeningTheTool(t *testing.T) {
	home, env := machine(t, true)
	work := t.TempDir()

	// A fake codex on the PATH that prints what it was given. That is the whole
	// of what `run` has to get right: the directory, the environment, and the
	// arguments after --.
	bin := filepath.Join(home, "bin")
	fake := filepath.Join(bin, "codex")
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a shell script")
	}
	write(t, fake, "#!/bin/sh\necho \"tool ran in $(pwd)\"\necho \"agent=$MNEMO_AGENT tool=$MNEMO_TOOL\"\necho \"args=$*\"\n")
	if err := os.Chmod(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	onPath := append([]string{}, env...)
	for i, entry := range onPath {
		if strings.HasPrefix(entry, "PATH=") {
			onPath[i] = "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
		}
	}
	codexHome := filepath.Join(work, "codex")
	onPath = append(onPath, "CODEX_HOME="+codexHome)

	// No name at all.
	out, code := runMnemo(t, onPath, "run", "codex", work)
	if code != 2 || !strings.Contains(out, "--name") {
		t.Errorf("without a name it exited %d:\n%s", code, out)
	}
	// A name that cannot be an address.
	out, code = runMnemo(t, onPath, "run", "codex", work, "--name", "Not A Name")
	if code != 2 || !strings.Contains(out, "not a usable name") {
		t.Errorf("with a bad name it exited %d:\n%s", code, out)
	}
	// A tool mnemo does not know.
	out, code = runMnemo(t, onPath, "run", "emacs", work, "--name", "alex")
	if code != 2 || !strings.Contains(out, "not a tool mnemo knows") {
		t.Errorf("with an unknown tool it exited %d:\n%s", code, out)
	}
	// A directory that is not one.
	out, code = runMnemo(t, onPath, "run", "codex", filepath.Join(work, "nowhere"), "--name", "alex")
	if code != 1 || !strings.Contains(out, "is not a directory") {
		t.Errorf("with a missing directory it exited %d:\n%s", code, out)
	}
	// Not registered: the chat would open with no memory at all, and the failure
	// is silent from inside it.
	out, code = runMnemo(t, onPath, "run", "codex", work, "--name", "alex")
	if code != 1 || !strings.Contains(out, "mnemo mcp add codex") {
		t.Errorf("unregistered it exited %d:\n%s", code, out)
	}

	if out, code := runMnemo(t, onPath, "mcp", "add", "codex"); code != 0 {
		t.Fatalf("registering: %d\n%s", code, out)
	}

	// Not on the PATH.
	withoutTool := append([]string{}, onPath...)
	for i, entry := range withoutTool {
		if strings.HasPrefix(entry, "PATH=") {
			withoutTool[i] = "PATH=" + filepath.Join(home, "empty")
		}
	}
	out, code = runMnemo(t, withoutTool, "run", "codex", work, "--name", "alex")
	if code != 1 || !strings.Contains(out, "is not on the PATH") {
		t.Errorf("without the tool it exited %d:\n%s", code, out)
	}

	// And then it runs, in the directory it was given, with the name and the
	// tool in the environment and the arguments after -- passed on.
	out, code = runMnemo(t, onPath, "run", "codex", work, "--name", "alex", "--", "--resume", "last")
	if code != 0 {
		t.Fatalf("opening: %d\n%s", code, out)
	}
	for _, want := range []string{
		"Opening codex as alex@testbox in " + work,
		"tool ran in " + work,
		"agent=alex tool=codex",
		"args=--resume last",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("it does not say %q:\n%s", want, out)
		}
	}
}

// And the address has to be free, checked before a window opens rather than
// after, which is the whole reason this check is here as well as in the chat.
func TestRunRefusesANameAChatAlreadyHolds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a shell script")
	}
	home, env := machine(t, true)
	work := t.TempDir()
	mailboxDir := filepath.Join(home, "mailbox")
	env = append(env, "MNEMO_MAILBOX_DIR="+mailboxDir, "CODEX_HOME="+filepath.Join(work, "codex"))

	bin := filepath.Join(home, "bin")
	write(t, filepath.Join(bin, "codex"), "#!/bin/sh\necho ran\n")
	if err := os.Chmod(filepath.Join(bin, "codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			env[i] = "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
		}
	}
	if out, code := runMnemo(t, env, "mcp", "add", "codex"); code != 0 {
		t.Fatalf("registering: %d\n%s", code, out)
	}

	// A chat that really holds the name: a live process, which is this test.
	since := time.Now().UTC().Format(time.RFC3339Nano)
	write(t, filepath.Join(mailboxDir, "state.json"), fmt.Sprintf(`{"version":2,"next_seq":1,"addresses":{
		"alex@testbox":{"tool":"claude","floor":0,"cursor":0,"last_seen":%q,
		"holder":{"instance":"another-chat","via":"local","pid":%d,"since":%q}}}}`,
		since, os.Getpid(), since))

	out, code := runMnemo(t, env, "run", "codex", work, "--name", "alex")
	if code != 1 {
		t.Fatalf("it exited %d:\n%s", code, out)
	}
	// Named, with the tool and the time: somebody has to know which window to
	// close, and "another chat" does not tell them.
	if !strings.Contains(out, "alex@testbox is already open in claude since") {
		t.Errorf("it does not say who has the name:\n%s", out)
	}
	if strings.Contains(out, "ran") {
		t.Error("the tool was opened anyway")
	}
}

// Two processes sending at once lose nothing and share no seq.
//
// The seq is what every cursor is measured against, so two messages with the
// same one would make each shadow the other for everybody. Nothing in the files
// prevents it except the lock and the rule that the next seq is the larger of
// the counter and the log, and the only way to find out whether those hold is
// two real processes doing it at the same time.
func TestBurstsFromTwoProcessesLoseNothingAndShareNoSeq(t *testing.T) {
	home, env := machine(t, true)
	mailboxDir := filepath.Join(home, "mailbox")
	chat := func(name, tool string) *mcp.ClientSession {
		session, _ := serve(t, append(env,
			"MNEMO_MAILBOX_DIR="+mailboxDir, "MNEMO_AGENT="+name, "MNEMO_TOOL="+tool))
		return session
	}

	left, right := chat("left", "claude"), chat("right", "codex")
	reader := chat("reader", "opencode")
	_ = reader

	const each = 15
	var waiting sync.WaitGroup
	burst := func(session *mcp.ClientSession, from string) {
		defer waiting.Done()
		for i := 0; i < each; i++ {
			result := invoke(t, session, "mailbox_send", map[string]any{
				"to": "reader@testbox", "body": fmt.Sprintf("%s %d", from, i),
			})
			if result.IsError {
				t.Errorf("%s %d: %s", from, i, allText(result))
				return
			}
		}
	}
	waiting.Add(2)
	go burst(left, "left")
	go burst(right, "right")
	waiting.Wait()

	// Read until it stops handing things over: one read is bounded, so the whole
	// burst takes several.
	bodies := map[string]bool{}
	seqs := map[float64]string{}
	for round := 0; round < 10; round++ {
		result := invoke(t, reader, "mailbox_read", nil)
		if result.IsError {
			t.Fatalf("reading: %s", allText(result))
		}
		var answer struct {
			Messages []struct {
				Seq  float64 `json:"seq"`
				Body string  `json:"body"`
			} `json:"messages"`
			Left int `json:"left"`
		}
		// The client decodes it into a map, so it goes back through JSON to be
		// read as the shape the spec gives.
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &answer); err != nil {
			t.Fatalf("the structured answer is %s: %v", raw, err)
		}
		for _, message := range answer.Messages {
			if was, seen := seqs[message.Seq]; seen {
				t.Errorf("seq %v is both %q and %q", message.Seq, was, message.Body)
			}
			seqs[message.Seq] = message.Body
			bodies[message.Body] = true
		}
		if answer.Left == 0 {
			break
		}
	}

	if len(bodies) != 2*each {
		var missing []string
		for _, from := range []string{"left", "right"} {
			for i := 0; i < each; i++ {
				if body := fmt.Sprintf("%s %d", from, i); !bodies[body] {
					missing = append(missing, body)
				}
			}
		}
		sort.Strings(missing)
		t.Errorf("%d of %d arrived; missing %v", len(bodies), 2*each, missing)
	}
}

// Retiring a store is the one destructive operation no agent can reach, so what
// it asks for is the store's own path. A yes is too easy to give by accident for
// something with no undo.
func TestStoreRemoveShowsWhatWouldGoAndAsksForThePath(t *testing.T) {
	home, env := machine(t, true)
	storeDir := filepath.Join(home, "store")

	// What it shows has to be enough to decide on: the path, the projects by
	// name with their own counts, and whether anything else has a copy.
	out, code := runMnemoWith(t, env, "", "store", "remove")
	if code != 0 {
		t.Fatalf("it exited %d:\n%s", code, out)
	}
	for _, want := range []string{
		"store: " + storeDir,
		"projects: 1",
		"shop",
		"1 memory",
		"memories: 1",
		"hub: none — these memories exist only here",
		"the store is moved aside, next to itself, not deleted",
		"type the store's path to go ahead",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("it does not say %q:\n%s", want, out)
		}
	}
	// Nothing typed is nothing done.
	if !strings.Contains(out, "that is not the store's path. Nothing was done.") {
		t.Errorf("an empty answer was not refused:\n%s", out)
	}
	if _, err := os.Stat(storeDir); err != nil {
		t.Fatalf("the store went anyway: %v", err)
	}

	// A yes is not the path.
	out, _ = runMnemoWith(t, env, "yes\n", "store", "remove")
	if !strings.Contains(out, "Nothing was done.") {
		t.Errorf("\"yes\" was taken as the path:\n%s", out)
	}
	if _, err := os.Stat(storeDir); err != nil {
		t.Fatalf("the store went on a yes: %v", err)
	}

	// The path, and this store has no hub, so it is the only copy and that
	// stops it. The refusal says what would be lost.
	out, code = runMnemoWith(t, env, storeDir+"\n", "store", "remove")
	if code != 1 {
		t.Fatalf("it exited %d with no hub:\n%s", code, out)
	}
	if !strings.Contains(out, "no hub") || !strings.Contains(out, "--force") {
		t.Errorf("the refusal reads:\n%s", out)
	}
	if _, err := os.Stat(storeDir); err != nil {
		t.Fatalf("the only copy was retired: %v", err)
	}

	// And with --force it goes, moved aside rather than deleted, and the output
	// says where it went and the one line that ends it for good.
	out, code = runMnemoWith(t, env, storeDir+"\n", "store", "remove", "--force")
	if code != 0 {
		t.Fatalf("it exited %d:\n%s", code, out)
	}
	if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
		t.Errorf("the store is still there: %v", err)
	}
	moved := ""
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "store.retired-") {
			moved = filepath.Join(home, entry.Name())
		}
	}
	if moved == "" {
		t.Fatalf("the store was not moved aside, it is simply gone:\n%s", out)
	}
	if !strings.Contains(out, "moved "+storeDir+" to "+moved) {
		t.Errorf("it does not say where it went:\n%s", out)
	}
	if !strings.Contains(out, "rm -rf '"+moved+"'") {
		t.Errorf("it does not give the line that deletes it:\n%s", out)
	}
	// What is left is a store with everything in it, under another name.
	if _, err := os.Stat(filepath.Join(moved, "memories", "a-decision.md")); err != nil {
		t.Errorf("the memories did not come with it: %v", err)
	}
	// And the config is untouched, which is what makes the next save start a
	// fresh store rather than nothing at all.
	if !strings.Contains(out, "the config is unchanged") {
		t.Errorf("it does not say the config is still there:\n%s", out)
	}
	// A store that is not there says so, with the path.
	out, code = runMnemoWith(t, env, "", "store", "remove")
	if code != 1 || !strings.Contains(out, "There is no store at "+storeDir) {
		t.Errorf("a missing store exited %d with:\n%s", code, out)
	}
}

// --purge deletes instead of moving aside, and says so before it asks.
func TestStoreRemovePurgeSaysItIsDeletingBeforeItAsks(t *testing.T) {
	home, env := machine(t, true)
	storeDir := filepath.Join(home, "store")

	out, code := runMnemoWith(t, env, storeDir+"\n", "store", "remove", "--purge", "--force")
	if code != 0 {
		t.Fatalf("it exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "this deletes the store outright, and there is no undo") {
		t.Errorf("it does not say what --purge does, before asking:\n%s", out)
	}
	if !strings.Contains(out, "deleted "+storeDir) {
		t.Errorf("it does not say what it did:\n%s", out)
	}
	if _, err := os.Stat(storeDir); !os.IsNotExist(err) {
		t.Errorf("the store is still there: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "store.retired-") {
			t.Errorf("--purge left a copy behind at %s", entry.Name())
		}
	}
}

// runMnemoWith is runMnemo with something on stdin, for the one command that
// asks a question.
func runMnemoWith(t *testing.T, env []string, stdin string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), runWait)
	defer cancel()
	command := exec.CommandContext(ctx, built(t), args...)
	command.Env = env
	command.Stdin = strings.NewReader(stdin)
	out, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("mnemo %v was still running after %s:\n%s", args, runWait, out)
	}
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("mnemo %v: %v", args, err)
	}
	return string(out), code
}

// The server is the process a machine runs so that others can reach its memory.
// It is started by a service, which has none of the user's shell environment, so
// everything it needs comes from the config file.
func TestServingOverHTTPAnswersOnLoopbackBehindTheToken(t *testing.T) {
	home, env := machine(t, true)
	const token = "a-secret-for-this-test"
	port := freePortFor(t)
	write(t, filepath.Join(home, "config", "mnemo", "config.toml"),
		fmt.Sprintf("[server]\nport = %d\ntoken = %q\n", port, token))

	command := exec.Command(built(t), "serve", "--http")
	command.Env = env
	said := &complaints{}
	command.Stderr = said
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		_ = command.Wait()
	})

	address := fmt.Sprintf("http://127.0.0.1:%d/v1/health", port)
	client := &http.Client{Timeout: 2 * time.Second}
	var response *http.Response
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		if response, err = client.Do(request); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if response == nil {
		t.Fatalf("the server never answered on %s\nstderr:\n%s", address, said)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"api":`) {
		t.Fatalf("%d: %s", response.StatusCode, body)
	}

	// Without the token, nothing — which is the whole of what stands between
	// another process on this machine and the user's memory.
	plain, err := client.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	plain.Body.Close()
	if plain.StatusCode != http.StatusUnauthorized {
		t.Errorf("without the token it answered %d", plain.StatusCode)
	}

	// What it says on stderr is what somebody reading a service log needs: the
	// version, where it listens, and whose files it answers for.
	for _, want := range []string{"api 1", fmt.Sprintf("127.0.0.1:%d", port), "store:", "mailbox:"} {
		if !strings.Contains(said.String(), want) {
			t.Errorf("the log does not say %q:\n%s", want, said)
		}
	}
	// And the token is never in it. A log is the one place a secret ends up by
	// accident and stays for ever.
	if strings.Contains(said.String(), token) {
		t.Errorf("the token is in the log:\n%s", said)
	}
}

// A machine with no server section has nothing to serve, and says so rather than
// listening on a default port with no token.
func TestServingOverHTTPWithoutAServerRefuses(t *testing.T) {
	_, env := machine(t, true)
	out, code := runMnemo(t, env, "serve", "--http")
	if code != 1 {
		t.Fatalf("it exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "mnemo server setup") {
		t.Errorf("the refusal does not say what to do:\n%s", out)
	}
}

// freePortFor is a port nothing is listening on. The server is told which port
// to use because a test cannot use the real one: another mnemo may be running.
func freePortFor(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
