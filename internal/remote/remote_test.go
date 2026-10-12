package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AlexParco/mnemo/internal/build"
	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/httpapi"
	"github.com/AlexParco/mnemo/internal/lock"
	"github.com/AlexParco/mnemo/internal/mailbox"
	"github.com/AlexParco/mnemo/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverToken = "the-servers-own-token"

// pair is a real server and a client pointed at it.
//
// The server is the actual internal/httpapi, not a stand-in: what is being
// tested is whether the two halves of a connection agree, and two halves tested
// against each other's imitations agree about the imitation.
func pair(t *testing.T, name string) (*httptest.Server, *Client, *mailbox.Mailbox) {
	t.Helper()
	home := t.TempDir()
	settings := config.Settings{
		Machine: "vps",
		Server:  config.Hosted{Port: 7433, Token: serverToken},
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
	if err := os.WriteFile(filepath.Join(settings.Paths.Store, "projects", "shop", "INDEX.md"),
		[]byte("---\nslug: shop\nname: Shop\nstatus: active\nupdated: 2026-01-02\n---\n\nThe shop.\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	locker := lock.New(settings.Paths.Locks)
	box := mailbox.New(settings.Paths.Mailbox, settings.Machine, mailbox.Options{Locker: locker})
	api, err := httpapi.New(httpapi.Options{
		Settings: settings,
		Store:    store.New(settings.Paths.Store, store.Options{Locker: locker}),
		Mailbox:  box,
	})
	if err != nil {
		t.Fatal(err)
	}
	running := httptest.NewServer(api.Handler())
	t.Cleanup(running.Close)

	return running, clientFor(t, running, name), box
}

// clientFor points a client at a running server. The connection's local port is
// the test server's, which stands for this end of the tunnel.
func clientFor(t *testing.T, running *httptest.Server, name string) *Client {
	t.Helper()
	address := strings.TrimPrefix(running.URL, "http://")
	port, err := strconv.Atoi(address[strings.LastIndex(address, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	caller := mailbox.Caller{
		Machine: "laptop1", Tool: "claude", Instance: "1111111111111111", Via: "remote", PID: 4242,
	}
	if name != "" {
		caller.Name = name
	}
	return New(config.Connection{
		Machine: "vps", SSH: "me@vps", ServerPort: 7433, LocalPort: port,
		Token: serverToken, Connected: true,
	}, caller)
}

// The check every part makes before it trusts the server, and the api number is
// the reason it exists.
func TestReachableChecksTheApiAndNotOnlyThatSomethingAnswered(t *testing.T) {
	running, client, _ := pair(t, "")

	health, err := client.Reachable(t.Context())
	if err != nil {
		t.Fatalf("a server that is there: %v", err)
	}
	if health.API != build.API || health.Machine != "vps" {
		t.Errorf("the health is %+v", health)
	}

	// A server speaking another api is refused by number, and the refusal says
	// which machine to update — the one that is older, which is the only thing
	// a person can act on.
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"version":"9.9.9","api":%d,"machine":"vps"}`, build.API+1)
	}))
	defer elsewhere.Close()
	other := clientFor(t, elsewhere, "")
	if _, err := other.Reachable(t.Context()); err == nil {
		t.Error("a server with another api was accepted")
	} else {
		for _, want := range []string{"api", "mnemo update", "vps"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not say %q: %v", want, err)
			}
		}
	}

	// A token the server does not know is its own refusal, and it says what to
	// do: the code was probably rotated.
	wrong := clientFor(t, running, "")
	wrong.Connection.Token = "not-the-token"
	if _, err := wrong.Reachable(t.Context()); err == nil {
		t.Error("a wrong token was accepted")
	} else if !strings.Contains(err.Error(), "mnemo connect") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}

	// And nothing listening is NotSent: the request never left, so nothing was
	// done, here or there.
	gone := clientFor(t, running, "")
	gone.Connection.LocalPort = freePort(t)
	_, err = gone.Reachable(t.Context())
	var notSent *NotSent
	if err == nil || !asNotSent(err, &notSent) {
		t.Fatalf("a dead port gave %v", err)
	}
	if !strings.Contains(notSent.Error(), "Nothing was done, here or there") {
		t.Errorf("it reads: %v", notSent)
	}
}

// A name is claimed on the server, renewed while the chat is open, and given
// back when it closes. The server frees it on a lease because it cannot see this
// machine's processes.
func TestAHeartbeatHoldsTheNameAndAReleaseGivesItBack(t *testing.T) {
	running, client, box := pair(t, "chat-a")

	refusal, err := client.Heartbeat(t.Context())
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if refusal != "" {
		t.Fatalf("the first claim was refused: %s", refusal)
	}
	occupant, live, err := box.Occupied(t.Context(), "chat-a@laptop1")
	if err != nil {
		t.Fatal(err)
	}
	if !live || occupant.Via != "remote" || occupant.Tool != "claude" {
		t.Errorf("the server holds %+v live=%v", occupant, live)
	}

	// A second chat on this machine with the same name is refused, and the
	// refusal is handed back as a message rather than as an error: losing the
	// mailbox is not losing the memory, so the chat carries on and the identity
	// line says why.
	second := clientFor(t, running, "chat-a")
	second.Caller.Instance = "2222222222222222"
	second.Caller.Tool = "codex"
	refusal, err = second.Heartbeat(t.Context())
	if err != nil {
		t.Fatalf("a refused claim came back as an error: %v", err)
	}
	if !strings.Contains(refusal, "already open in claude") {
		t.Errorf("the refusal reads %q", refusal)
	}

	if err := client.Release(t.Context()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, live, err := box.Occupied(t.Context(), "chat-a@laptop1"); err != nil {
		t.Fatal(err)
	} else if live {
		t.Error("the name is still held after the chat released it")
	}
}

// What `mnemo run` asks before it opens a chat on this machine, and what a
// watcher asks every two seconds.
func TestTheClientAsksWhoHoldsANameAndWhatIsWaiting(t *testing.T) {
	running, client, box := pair(t, "chat-a")
	client.Heartbeat(t.Context())

	holder, err := client.Holding(t.Context(), "chat-a@laptop1")
	if err != nil {
		t.Fatal(err)
	}
	if !holder.Held || holder.Tool != "claude" {
		t.Errorf("the holder is %+v", holder)
	}
	// The same sentence a person reads when the name is held on this machine:
	// one wording for one fact.
	if said := holder.Describe(); !strings.Contains(said, "claude on another machine since") {
		t.Errorf("it describes the holder as %q", said)
	}
	if free, err := client.Holding(t.Context(), "nobody@laptop1"); err != nil {
		t.Fatal(err)
	} else if free.Held {
		t.Errorf("a name nobody holds is %+v", free)
	}

	sender := mailbox.Caller{Name: "boss", Machine: "vps", Tool: "claude",
		Instance: "3333333333333333", Via: "local", PID: os.Getpid()}
	if _, err := box.Claim(t.Context(), sender); err != nil {
		t.Fatal(err)
	}
	if _, err := box.Send(t.Context(), sender, "chat-a@laptop1", "the parser is done", ""); err != nil {
		t.Fatal(err)
	}

	waiting, err := client.Unread(t.Context(), "chat-a@laptop1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.LastSeq != 1 || len(waiting.Messages) != 1 {
		t.Fatalf("what is waiting is %+v", waiting)
	}
	if waiting.Messages[0].Body != "the parser is done" {
		t.Errorf("the message is %+v", waiting.Messages[0])
	}
	// A peek marks nothing: asked again, it is still there.
	if again, err := client.Unread(t.Context(), "chat-a@laptop1", 0); err != nil {
		t.Fatal(err)
	} else if len(again.Messages) != 1 {
		t.Errorf("asking consumed it: %+v", again)
	}
	_ = running
}

// The relay is what a coding tool starts on a connected machine. It offers the
// same tools as a local mnemo and every call goes to the server.
func TestTheRelayOffersTheSameToolsAndForwardsEveryCall(t *testing.T) {
	_, client, box := pair(t, "chat-a")
	relay := NewRelay(client)
	defer relay.Close()

	session := connected(t, relay)

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	offered := map[string]bool{}
	for _, tool := range listed.Tools {
		offered[tool.Name] = true
	}
	for _, want := range []string{"mnemo_status", "mnemo_load_project", "mailbox_peers", "mailbox_send"} {
		if !offered[want] {
			t.Errorf("the relay does not offer %s", want)
		}
	}

	// A call lands on the server: the answer is about the server's files and
	// says so, and it treats this machine as the caller.
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mnemo_status"})
	if err != nil {
		t.Fatalf("a forwarded call: %v", err)
	}
	said := textOf(t, result)
	if !strings.Contains(said, "served by: vps") {
		t.Errorf("the answer is not the server's:\n%s", said)
	}
	if !strings.Contains(said, "machine: laptop1") {
		t.Errorf("the answer does not take this machine as the caller:\n%s", said)
	}

	// And a mailbox call really reaches the server's mailbox, which is the one
	// thing a connected machine must never do locally.
	sender := mailbox.Caller{Name: "boss", Machine: "vps", Tool: "claude",
		Instance: "3333333333333333", Via: "local", PID: os.Getpid()}
	if _, err := box.Claim(t.Context(), sender); err != nil {
		t.Fatal(err)
	}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "mailbox_send",
		Arguments: map[string]any{"to": "boss@vps", "body": "from the other machine"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("sending through the relay: %s", textOf(t, result))
	}
	delivered, err := box.Read(t.Context(), sender)
	if err != nil {
		t.Fatal(err)
	}
	if len(delivered.Messages) != 1 || delivered.Messages[0].From != "chat-a@laptop1" {
		t.Fatalf("the server's mailbox holds %+v", delivered.Messages)
	}
}

// What the server says comes back exactly as the server said it, including a
// refusal: a relay that reinterpreted an answer would be a second place where
// the contract lives.
func TestARefusalFromTheServerArrivesUnchanged(t *testing.T) {
	_, client, _ := pair(t, "chat-a")
	relay := NewRelay(client)
	defer relay.Close()
	session := connected(t, relay)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "mailbox_send",
		Arguments: map[string]any{"to": "nobody-at-all", "body": "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("a send to nobody was accepted")
	}
	said := textOf(t, result)
	if !strings.Contains(said, "Nobody called 'nobody-at-all' has appeared yet") {
		t.Errorf("the refusal reads:\n%s", said)
	}
}

// A call that cannot reach the server is a refusal that says which of the two
// things happened, because the agent has to do a different thing about each.
func TestACallThatCannotReachTheServerSaysWhetherItWasSent(t *testing.T) {
	_, client, _ := pair(t, "chat-a")
	client.Connection.LocalPort = freePort(t)
	woken := 0
	relay := NewRelay(client)
	relay.WakeTunnel = func(context.Context) { woken++ }
	defer relay.Close()

	result, err := relay.Call(t.Context(), "mnemo_status", nil)
	if err == nil {
		t.Fatalf("a dead server answered: %+v", result)
	}
	var notSent *NotSent
	if !asNotSent(err, &notSent) {
		t.Fatalf("it gave %T: %v", err, err)
	}
	for _, want := range []string{"cannot reach the server on vps", "Nothing was done, here or there",
		"mnemo connect"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}

	// The tunnel is prodded, and a run of failures is one problem rather than
	// twenty: starting the keeper per failed call would launch a process for
	// each one, and they would all find the lock taken anyway.
	for i := 0; i < 5; i++ {
		relay.Call(t.Context(), "mnemo_status", nil)
	}
	if woken != 1 {
		t.Errorf("the tunnel was prodded %d times for one outage", woken)
	}
}

// A chat with a name holds it while it is open, and gives it back when stdin
// closes — and a chat whose name is somebody else's is told, without losing its
// memory over it.
func TestTheRelayHoldsTheNameWhileTheChatIsOpen(t *testing.T) {
	running, client, box := pair(t, "chat-a")

	// Somebody else already has the name on the server.
	other := clientFor(t, running, "chat-a")
	other.Caller.Instance = "9999999999999999"
	other.Caller.Tool = "codex"
	if refusal, err := other.Heartbeat(t.Context()); err != nil || refusal != "" {
		t.Fatalf("the first claim: refusal=%q err=%v", refusal, err)
	}

	relay := NewRelay(client)
	serving, stop := context.WithCancel(t.Context())
	ours, theirs := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- relay.Serve(serving, &mcp.IOTransport{Reader: theirs, Writer: theirs}) }()

	// The claim happens before the handshake is answered, so by the time a tool
	// could ask, the refusal is known.
	waitFor(t, func() bool { return relay.Refused() != "" })
	if said := relay.Refused(); !strings.Contains(said, "already open in codex") {
		t.Errorf("the chat was told %q", said)
	}

	stop()
	ours.Close()
	theirs.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not stop when the chat went away")
	}

	// The name that was not ours is still theirs: a chat that was refused must
	// not release somebody else's hold on the way out.
	if occupant, live, err := box.Occupied(context.Background(), "chat-a@laptop1"); err != nil {
		t.Fatal(err)
	} else if !live || occupant.Tool != "codex" {
		t.Errorf("after our chat closed the holder is %+v live=%v", occupant, live)
	}
}

// connected opens an in-process MCP client against a relay, which is what a
// coding tool does over stdio.
func connected(t *testing.T, relay *Relay) *mcp.ClientSession {
	t.Helper()
	ours, theirs := net.Pipe()
	go func() {
		// The relay owns one side; its failure is the test's failure only if
		// the client cannot talk to it, which the client reports itself.
		_ = relay.Serve(t.Context(), &mcp.IOTransport{Reader: theirs, Writer: theirs})
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "a-coding-tool", Version: "v0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: ours, Writer: ours}, nil)
	if err != nil {
		t.Fatalf("connecting to the relay: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func textOf(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	var said []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			said = append(said, text.Text)
		}
	}
	return strings.Join(said, "\n")
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("it never happened")
}

// freePort is a port nothing is listening on, for the failures that have to be
// tested: a tunnel that is down looks exactly like this.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// asNotSent is errors.As, named for what it asks: did the request never leave.
func asNotSent(err error, into **NotSent) bool { return errors.As(err, into) }
