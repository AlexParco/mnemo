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
