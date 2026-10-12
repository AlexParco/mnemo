package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexParco/mnemo/internal/build"
	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/lock"
	"github.com/AlexParco/mnemo/internal/mailbox"
	"github.com/AlexParco/mnemo/internal/store"
)

const token = "a-token-nobody-else-has"

// served is a running server and the address it answers on. Everything here
// goes through a real HTTP request, because what is being tested is the
// interface another machine sees and not the functions behind it.
func served(t *testing.T) (*httptest.Server, *mailbox.Mailbox) {
	t.Helper()
	home := t.TempDir()
	settings := config.Settings{
		Machine: "vps",
		Server:  config.Hosted{Port: 7433, Token: token},
		Paths: config.Paths{
			Store:   filepath.Join(home, "store"),
			State:   home,
			Locks:   filepath.Join(home, "locks"),
			Mailbox: filepath.Join(home, "mailbox"),
		},
	}
	for _, sub := range []string{filepath.Join("store", "projects", "shop"),
		filepath.Join("store", "memories"), filepath.Join("store", "shared")} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(settings.Paths.Store, "projects", "shop", "INDEX.md"),
		"---\nslug: shop\nname: Shop\nstatus: active\nupdated: 2026-01-02\n---\n\nThe shop.\n")

	locker := lock.New(settings.Paths.Locks)
	box := mailbox.New(settings.Paths.Mailbox, settings.Machine, mailbox.Options{
		Locker: locker,
		// A remote holder lives on a lease, so nothing here asks about a
		// process. The answer is given anyway, to be sure no path falls back
		// to asking.
		Alive: func(int) bool { t.Error("the server asked whether a remote process is alive"); return false },
	})
	server, err := New(Options{
		Settings: settings,
		Store:    store.New(settings.Paths.Store, store.Options{Locker: locker}),
		Mailbox:  box,
	})
	if err != nil {
		t.Fatal(err)
	}
	running := httptest.NewServer(server.Handler())
	t.Cleanup(running.Close)
	return running, box
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

// ask makes a request with the token, and the caller headers when there are any.
func ask(t *testing.T, running *httptest.Server, method, path, body string,
	headers map[string]string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, running.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := running.Client().Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { response.Body.Close() })
	said, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, string(said)
}

func decoded(t *testing.T, said string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(said), &out); err != nil {
		t.Fatalf("the body is not JSON: %q", said)
	}
	return out
}

// Nothing is answered without the token, and nothing it protects is touched.
func TestWithoutTheTokenNothingIsAnswered(t *testing.T) {
	running, _ := served(t)

	for _, header := range []string{"", "Bearer", "Bearer ", "Basic abc", "Bearer wrong-token",
		"Bearer " + token + "x"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, running.URL+"/v1/health", nil)
		if err != nil {
			t.Fatal(err)
		}
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		response, err := running.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		said, _ := io.ReadAll(response.Body)
		response.Body.Close()

		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authorization %q got %d", header, response.StatusCode)
		}
		// The header is what tells a client the token is the problem, which is
		// the difference between retrying and asking the user to connect again.
		if got := response.Header.Get("WWW-Authenticate"); got != `Bearer realm="mnemo"` {
			t.Errorf("Authorization %q got WWW-Authenticate %q", header, got)
		}
		if !strings.Contains(string(said), `"error"`) {
			t.Errorf("Authorization %q got body %q", header, said)
		}
		// Nothing of the server's own is in it: not the token, not a length,
		// not the machine's name.
		if strings.Contains(string(said), token) {
			t.Errorf("the refusal carries the token: %q", said)
		}
	}

	// The scheme is case-insensitive in HTTP, and a client that sends `bearer`
	// is not wrong.
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, running.URL+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "bearer "+token)
	response, err := running.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("a lower-case scheme got %d", response.StatusCode)
	}
}

// Health is what every client checks first, and the api number is the reason: a
// server that speaks a different one answers in a shape the client cannot read.
func TestHealthSaysTheVersionTheApiAndTheMachine(t *testing.T) {
	running, _ := served(t)

	response, said := ask(t, running, http.MethodGet, "/v1/health", "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", response.StatusCode, said)
	}
	body := decoded(t, said)
	if body["version"] != build.Version {
		t.Errorf("version is %v", body["version"])
	}
	if body["api"] != float64(build.API) {
		t.Errorf("api is %v, want the number %d", body["api"], build.API)
	}
	if body["machine"] != "vps" {
		t.Errorf("machine is %v", body["machine"])
	}
}

// A name is held by an instance on another machine, on a lease, because this
// machine cannot see that machine's processes.
func TestAHeartbeatClaimsANameAndSaysTheLease(t *testing.T) {
	running, box := served(t)

	response, said := ask(t, running, http.MethodPost, "/v1/mailbox/heartbeat",
		`{"agent":"chat-a","machine":"laptop1","tool":"claude","instance":"1111111111111111","pid":51234}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", response.StatusCode, said)
	}
	body := decoded(t, said)
	if body["address"] != "chat-a@laptop1" {
		t.Errorf("address is %v", body["address"])
	}
	if body["lease_seconds"] != float64(mailbox.Lease.Seconds()) {
		t.Errorf("lease_seconds is %v, want %v", body["lease_seconds"], mailbox.Lease.Seconds())
	}

	// The same instance again is a renewal, not a collision.
	response, said = ask(t, running, http.MethodPost, "/v1/mailbox/heartbeat",
		`{"agent":"chat-a","machine":"laptop1","tool":"claude","instance":"1111111111111111","pid":51234}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("a renewal got %d: %s", response.StatusCode, said)
	}

	// Another instance asking for the same name is refused, and told which chat
	// has it and since when — the same facts a local refusal gives.
	response, said = ask(t, running, http.MethodPost, "/v1/mailbox/heartbeat",
		`{"agent":"chat-a","machine":"laptop1","tool":"codex","instance":"2222222222222222"}`, nil)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("a second instance got %d: %s", response.StatusCode, said)
	}
	body = decoded(t, said)
	if !strings.Contains(fmt.Sprint(body["error"]), "already open in claude") {
		t.Errorf("the refusal reads %v", body["error"])
	}
	holder, ok := body["holder"].(map[string]any)
	if !ok || holder["tool"] != "claude" || holder["since"] == nil {
		t.Errorf("the holder is %v", body["holder"])
	}

	// And the name really is held, as the mailbox sees it.
	if occupant, live, err := box.Occupied(t.Context(), "chat-a@laptop1"); err != nil {
		t.Fatal(err)
	} else if !live || occupant.Via != "remote" {
		t.Errorf("the mailbox says live=%v occupant=%+v", live, occupant)
	}
}

// A client that goes away gives the name back, and it knows only its instance:
// that is all it ever sent.
func TestAReleaseGivesBackWhateverThatInstanceHeld(t *testing.T) {
	running, box := served(t)
	ask(t, running, http.MethodPost, "/v1/mailbox/heartbeat",
		`{"agent":"chat-a","machine":"laptop1","tool":"claude","instance":"1111111111111111"}`, nil)

	response, said := ask(t, running, http.MethodPost, "/v1/mailbox/release",
		`{"instance":"1111111111111111"}`, nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("%d: %s", response.StatusCode, said)
	}
	if _, live, err := box.Occupied(t.Context(), "chat-a@laptop1"); err != nil {
		t.Fatal(err)
	} else if live {
		t.Error("the name is still held after a release")
	}

	// Releasing something that was never held is not an error: a client that
	// crashed and came back has nothing to give up.
	response, said = ask(t, running, http.MethodPost, "/v1/mailbox/release",
		`{"instance":"9999999999999999"}`, nil)
	if response.StatusCode != http.StatusNoContent {
		t.Errorf("releasing nothing got %d: %s", response.StatusCode, said)
	}
}

// `mnemo run` on another machine asks this before it opens a chat.
func TestTheHolderEndpointSaysWhoHasAName(t *testing.T) {
	running, _ := served(t)

	response, said := ask(t, running, http.MethodGet, "/v1/mailbox/holder?address=chat-a@laptop1", "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", response.StatusCode, said)
	}
	if body := decoded(t, said); body["held"] != false {
		t.Errorf("a name nobody holds is %v", said)
	}

	ask(t, running, http.MethodPost, "/v1/mailbox/heartbeat",
		`{"agent":"chat-a","machine":"laptop1","tool":"claude","instance":"1111111111111111"}`, nil)

	_, said = ask(t, running, http.MethodGet, "/v1/mailbox/holder?address=chat-a@laptop1", "", nil)
	body := decoded(t, said)
	if body["held"] != true || body["tool"] != "claude" || body["since"] == nil {
		t.Errorf("a held name is %v", said)
	}

	// Without an address there is nothing to answer about.
	response, said = ask(t, running, http.MethodGet, "/v1/mailbox/holder", "", nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("no address got %d: %s", response.StatusCode, said)
	}
}

// What a watcher on another machine asks. It records nobody and marks nothing
// read: announcing is not delivering.
func TestUnreadIsAPeekAndNothingMore(t *testing.T) {
	running, box := served(t)
	sender := mailbox.Caller{Name: "boss", Machine: "vps", Tool: "claude",
		Instance: "3333333333333333", Via: "remote"}
	if _, err := box.Claim(t.Context(), sender); err != nil {
		t.Fatal(err)
	}
	ask(t, running, http.MethodPost, "/v1/mailbox/heartbeat",
		`{"agent":"chat-a","machine":"laptop1","tool":"claude","instance":"1111111111111111"}`, nil)
	if _, err := box.Send(t.Context(), sender, "chat-a@laptop1", "the parser is done", ""); err != nil {
		t.Fatal(err)
	}

	response, said := ask(t, running, http.MethodGet,
		"/v1/mailbox/unread?address=chat-a@laptop1&after=0", "", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", response.StatusCode, said)
	}
	body := decoded(t, said)
	if body["last_seq"] != float64(1) {
		t.Errorf("last_seq is %v", body["last_seq"])
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages is %v", body["messages"])
	}
	first := messages[0].(map[string]any)
	if first["body"] != "the parser is done" || first["from"] != "boss@vps" {
		t.Errorf("the message is %v", first)
	}

	// Asked again it is still there: a peek marks nothing.
	_, said = ask(t, running, http.MethodGet, "/v1/mailbox/unread?address=chat-a@laptop1", "", nil)
	if messages := decoded(t, said)["messages"].([]any); len(messages) != 1 {
		t.Errorf("a peek consumed the post: %v", messages)
	}

	// An empty result is an empty list, never null: a client iterates it.
	_, said = ask(t, running, http.MethodGet, "/v1/mailbox/unread?address=nobody@elsewhere", "", nil)
	if !strings.Contains(said, `"messages":[]`) {
		t.Errorf("an empty answer reads %q", said)
	}

	// after must be a number, and a wrong one is said rather than guessed at.
	response, said = ask(t, running, http.MethodGet,
		"/v1/mailbox/unread?address=chat-a@laptop1&after=soon", "", nil)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(said, "whole number") {
		t.Errorf("after=soon got %d: %s", response.StatusCode, said)
	}
}

// Who is asking comes from the headers, and the ones that decide what happens
// to somebody's work are required.
func TestAToolCallSaysWhichMachineAndWhichToolIsAsking(t *testing.T) {
	running, _ := served(t)
	const call = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

	for what, headers := range map[string]map[string]string{
		"no machine": {"X-Mnemo-Tool": "claude"},
		"no tool":    {"X-Mnemo-Machine": "laptop1"},
		"an agent with no instance": {
			"X-Mnemo-Machine": "laptop1", "X-Mnemo-Tool": "claude", "X-Mnemo-Agent": "chat-a",
		},
	} {
		headers["Content-Type"] = "application/json"
		headers["Accept"] = "application/json, text/event-stream"
		response, said := ask(t, running, http.MethodPost, "/mcp", call, headers)
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s got %d: %s", what, response.StatusCode, said)
		}
		if !strings.Contains(said, "X-Mnemo-") {
			t.Errorf("%s does not say which header is missing: %s", what, said)
		}
	}

	// With all of them, the tools come back — the same list the local server
	// offers, because the client refuses a server whose api differs.
	response, said := ask(t, running, http.MethodPost, "/mcp", call, map[string]string{
		"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
		"X-Mnemo-Machine": "laptop1", "X-Mnemo-Tool": "claude",
		"X-Mnemo-Agent": "chat-a", "X-Mnemo-Instance": "1111111111111111",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", response.StatusCode, said)
	}
	for _, want := range []string{"mnemo_load_project", "mailbox_send", "mnemo_status"} {
		if !strings.Contains(said, want) {
			t.Errorf("the tool list does not offer %s:\n%s", want, said)
		}
	}
	// Stateless: there is no session to lose, so a server that restarts does
	// not break a chat that is open on another machine.
	if got := response.Header.Get("Mcp-Session-Id"); got != "" {
		t.Errorf("the server handed out a session id: %q", got)
	}
}

// A wrong path and a wrong method are answered in the same shape as everything
// else, because a client parses these.
func TestAWrongPathAndAWrongMethodAreSaidInTheSameShape(t *testing.T) {
	running, _ := served(t)

	response, said := ask(t, running, http.MethodGet, "/v1/nothing-here", "", nil)
	if response.StatusCode != http.StatusNotFound || !strings.Contains(said, "/v1/nothing-here") {
		t.Errorf("an unknown path got %d: %s", response.StatusCode, said)
	}

	response, said = ask(t, running, http.MethodGet, "/v1/mailbox/heartbeat", "", nil)
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("a GET on heartbeat got %d: %s", response.StatusCode, said)
	}
	if got := response.Header.Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow is %q", got)
	}

	// A body that is not what the endpoint expects is said, not guessed at.
	response, said = ask(t, running, http.MethodPost, "/v1/mailbox/release", "{not json", nil)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(said, "JSON") {
		t.Errorf("a broken body got %d: %s", response.StatusCode, said)
	}
}

// A machine with no server has nothing to serve, and half a server is not one.
func TestWithoutAServerThereIsNothingToStart(t *testing.T) {
	for what, hosted := range map[string]config.Hosted{
		"nothing":       {},
		"a port alone":  {Port: 7433},
		"a token alone": {Token: token},
	} {
		if _, err := New(Options{Settings: config.Settings{Server: hosted}}); err == nil {
			t.Errorf("%s was accepted as a server", what)
		} else if !strings.Contains(err.Error(), "mnemo server setup") {
			t.Errorf("%s was refused with %q, which does not say what to do", what, err)
		}
	}
}

// The address is loopback and nothing else: the one thing protecting the store
// is that the port cannot be reached without first getting onto the machine.
func TestItListensOnLoopbackOnly(t *testing.T) {
	server, err := New(Options{Settings: config.Settings{
		Machine: "vps", Server: config.Hosted{Port: 7433, Token: token},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := server.Address(); got != "127.0.0.1:7433" {
		t.Errorf("it would listen on %q", got)
	}
}

// A body nobody asked for must not be able to make the server hold it in memory.
func TestABodyOverTheLimitIsRefused(t *testing.T) {
	running, _ := served(t)
	huge := bytes.Repeat([]byte("x"), 5<<20)

	response, said := ask(t, running, http.MethodPost, "/v1/mailbox/release",
		`{"instance":"`+string(huge)+`"}`, nil)
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a 5 MiB body got %d: %s", response.StatusCode, said)
	}
}

// Nothing in the server's own answers carries the token, which is the one secret
// that has to travel.
func TestNoAnswerCarriesTheToken(t *testing.T) {
	running, _ := served(t)
	for _, path := range []string{"/v1/health", "/v1/mailbox/holder?address=a@b", "/v1/nothing"} {
		_, said := ask(t, running, http.MethodGet, path, "", nil)
		if strings.Contains(said, token) {
			t.Errorf("%s answered with the token in it: %s", path, said)
		}
	}
}

// Timing must not tell a caller how close it got: both tokens are hashed and the
// hashes compared in constant time, so neither the length nor the content of the
// real one leaks.
func TestARejectionDoesNotDependOnHowWrongTheTokenIs(t *testing.T) {
	server, err := New(Options{Settings: config.Settings{
		Machine: "vps", Server: config.Hosted{Port: 7433, Token: token},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	// What is checked is the shape of the comparison, not the clock: a timing
	// measurement here would be a flaky test on a shared machine. Every one of
	// these is rejected, including one that shares a long prefix with the real
	// token and one far longer than it.
	for _, wrong := range []string{
		"", "a", token[:len(token)-1], token + "x", strings.Repeat("x", 4096),
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
		request.Header.Set("Authorization", "Bearer "+wrong)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("token %q got %d", wrong, recorder.Code)
		}
	}
}

// The store and the mailbox are the server's own, and the caller's machine is
// the caller's: that is the whole difference between this and a local call, and
// it is what decides whose pending work is whose.
func TestTheCallerIsTheClientAndTheFilesAreTheServers(t *testing.T) {
	running, _ := served(t)
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"mnemo_status","arguments":{}}}`

	_, said := ask(t, running, http.MethodPost, "/mcp", call, map[string]string{
		"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
		"X-Mnemo-Machine": "laptop1", "X-Mnemo-Tool": "claude",
	})
	if !strings.Contains(said, "machine: laptop1") {
		t.Errorf("the answer does not take the caller's machine:\n%s", said)
	}
	// And it says whose files they are, so a person reading a status knows
	// which machine they are looking at.
	if !strings.Contains(said, "served by: vps") {
		t.Errorf("the answer does not say which machine serves it:\n%s", said)
	}
}

// A wait that the client gives up on may run here until its own timeout. That is
// accepted, and what must not happen is the server holding the lock for longer
// than the wait itself.
func TestAWaitDoesNotOutliveItsOwnTimeout(t *testing.T) {
	running, _ := served(t)
	ask(t, running, http.MethodPost, "/v1/mailbox/heartbeat",
		`{"agent":"chat-a","machine":"laptop1","tool":"claude","instance":"1111111111111111"}`, nil)

	started := time.Now()
	_, said := ask(t, running, http.MethodPost, "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mailbox_wait",`+
			`"arguments":{"timeout_ms":300}}}`, map[string]string{
			"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
			"X-Mnemo-Machine": "laptop1", "X-Mnemo-Tool": "claude",
			"X-Mnemo-Agent": "chat-a", "X-Mnemo-Instance": "1111111111111111",
		})
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("a 300 ms wait took %s", took)
	}
	if !strings.Contains(said, "Nothing arrived before the timeout") {
		t.Errorf("the wait came back with:\n%s", said)
	}
}
