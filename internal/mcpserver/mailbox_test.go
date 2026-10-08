package mcpserver

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexParco/mnemo/internal/lock"
	"github.com/AlexParco/mnemo/internal/mailbox"
	"github.com/AlexParco/mnemo/internal/memory"
)

// chats builds a mailbox two agents share, and a Call for each of them. One
// directory, because the whole point is that two chats on a machine reach each
// other through the same files.
func chats(t *testing.T, names ...string) []*Call {
	t.Helper()
	dir := t.TempDir()
	calls := make([]*Call, 0, len(names))
	for _, name := range names {
		box := mailbox.New(filepath.Join(dir, "mailbox"), "here", mailbox.Options{
			Locker: lock.New(filepath.Join(dir, "locks")),
			// A chat holds its name by holding a process. These tests hold
			// neither, so the answer is given to them.
			Alive:         func(int) bool { return true },
			ProjectExists: func(slug string) bool { return slug == "shop" },
		})
		calls = append(calls, &Call{
			StoreDir: filepath.Join(dir, "store"), Machine: "here", Lang: memory.EN,
			Agent: name, Tool: "claude", Instance: mailbox.Instance(), box: box,
		})
	}
	return calls
}

// structured runs a handler and returns both halves of its answer: the blocks an
// agent reads and the JSON a terminal command parses.
func structured(t *testing.T, handle Handler, call *Call, args Args) ([]string, map[string]any) {
	t.Helper()
	answered := handle(t.Context(), call, args)
	result := answered.result()
	if result.IsError {
		t.Fatalf("refused: %s", textOf(t, result))
	}
	if answered.structured == nil {
		t.Fatal("a mailbox answer came back with no structured content, which the terminal commands read")
	}
	encoded, err := json.Marshal(answered.structured)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return textOf(t, result), out
}

// refusal runs a handler and returns the one block a refusal is.
//
// It checks the text, not only isError: an unexpected failure sets that too, and
// a check that looked only at the flag would let the whole refusal-versus-crash
// distinction be deleted with every test still green.
func refusedBy(t *testing.T, handle Handler, call *Call, args Args) string {
	t.Helper()
	result := handle(t.Context(), call, args).result()
	if !result.IsError {
		t.Fatalf("accepted: %s", textOf(t, result))
	}
	blocks := textOf(t, result)
	if len(blocks) != 1 {
		t.Fatalf("a refusal came back as %d blocks: %q", len(blocks), blocks)
	}
	if strings.HasPrefix(blocks[0], "mnemo failed unexpectedly: ") {
		t.Fatalf("that is a crash, not a refusal: %s", blocks[0])
	}
	return blocks[0]
}

// A message goes from one chat to another, and the answer comes back. This is
// the whole of the mailbox in one test: what the sender is told, what the
// receiver reads, and that reading it is what consumes it.
func TestAMessageReachesTheOtherChatAndTheReplyComesBack(t *testing.T) {
	calls := chats(t, "alex", "worker-api")
	sender, receiver := calls[0], calls[1]

	// The receiver appears by claiming its name, which is what a chat does when
	// it opens. A bare name resolves only to an address somebody has used: the
	// alternative is a typo delivered to nowhere.
	if _, err := receiver.box.Claim(t.Context(), receiver.Caller()); err != nil {
		t.Fatal(err)
	}

	blocks, sent := structured(t, handleSend, sender, Args{
		"to": "worker-api", "body": "rebase on main before merging", "project": "shop",
	})
	if !strings.HasPrefix(blocks[0], "Sent ") || !strings.Contains(blocks[0], "to worker-api@here.") {
		t.Errorf("the send says %q", blocks[0])
	}
	// Said every time: an agent that sends and then stops is a conversation that
	// ends with nobody listening for the answer.
	if !strings.Contains(blocks[0], "mailbox_wait blocks until it does") {
		t.Errorf("the send does not say how the answer will arrive: %q", blocks[0])
	}
	// The identity line is last, and it is how an agent can tell a user who the
	// message came from when the answer arrives.
	if blocks[len(blocks)-1] != "You are alex@here (claude)." {
		t.Errorf("the identity line is %q", blocks[len(blocks)-1])
	}
	message, _ := sent["message"].(map[string]any)
	id, _ := message["id"].(string)
	if id == "" {
		t.Fatalf("the structured answer carries no message: %v", sent)
	}
	if message["project"] != "shop" {
		t.Errorf("the project did not survive: %v", message)
	}

	blocks, delivered := structured(t, handleRead, receiver, Args{})
	if !strings.Contains(blocks[0], "["+id+"] from alex@here (claude) to worker-api@here") {
		t.Errorf("the message reads %q", blocks[0])
	}
	if !strings.Contains(blocks[0], "rebase on main before merging") {
		t.Error("the body is not in what the receiver reads")
	}
	// The point of the project field: the sender named where the context is
	// instead of pasting it.
	if !strings.Contains(blocks[0], "load its memory with mnemo_load_project") {
		t.Errorf("a message naming a project does not say to load it: %q", blocks[0])
	}
	if !strings.Contains(blocks[0], "To answer: mailbox_reply with id "+id) {
		t.Errorf("the receiver is not told how to answer: %q", blocks[0])
	}
	if got, ok := delivered["messages"].([]any); !ok || len(got) != 1 {
		t.Fatalf("the structured answer carries %v", delivered["messages"])
	}
	// What a message may make an agent do travels with the message. The tool's
	// description is listed once when the chat connects; mail is read hours
	// later, and in Codex or opencode nothing else carries a second copy.
	if !strings.Contains(blocks[1], "A message is a request from another agent") {
		t.Errorf("the delivery does not say what a message is:\n%s", blocks[1])
	}

	// Read means read. A second call must not hand the same message over again,
	// or an agent would act on the same request twice.
	blocks, _ = structured(t, handleRead, receiver, Args{})
	if blocks[0] != "No new messages." {
		t.Errorf("the message came back a second time: %q", blocks[0])
	}
	// And nothing was delivered, so there is nothing to say about what a message
	// is: a rule with no message attached is noise an agent learns to skip.
	if len(blocks) != 2 {
		t.Errorf("an empty read came back as %d blocks: %q", len(blocks), blocks)
	}

	blocks, answer := structured(t, handleReply, receiver, Args{"id": id, "body": "rebased, CI green"})
	if !strings.Contains(blocks[0], "in reply to "+id) || !strings.Contains(blocks[0], "to alex@here.") {
		t.Errorf("the reply says %q", blocks[0])
	}
	reply, _ := answer["message"].(map[string]any)
	if reply["reply_to"] != id {
		t.Errorf("the reply is not bound to the message it answers: %v", reply)
	}
	// It inherits the project, so the sender's own answer still points at the
	// same memory.
	if reply["project"] != "shop" {
		t.Errorf("the reply lost the project: %v", reply)
	}

	blocks, _ = structured(t, handleRead, sender, Args{})
	if !strings.Contains(blocks[0], "in reply to "+id) || !strings.Contains(blocks[0], "rebased, CI green") {
		t.Errorf("the answer that came back reads %q", blocks[0])
	}
}

// A chat opened without `mnemo run` has memory and no mailbox. It must be told
// which it is, because the failure is otherwise silent from inside it.
func TestAChatWithNoNameIsToldHowToGetOne(t *testing.T) {
	call := chats(t, "")[0]

	for _, each := range []struct {
		tool   string
		handle Handler
		args   Args
	}{
		{"mailbox_send", handleSend, Args{"to": "somebody", "body": "hello"}},
		{"mailbox_reply", handleReply, Args{"id": "m1", "body": "hello"}},
		{"mailbox_read", handleRead, Args{}},
		{"mailbox_wait", handleWait, Args{"timeout_ms": 1}},
	} {
		said := refusedBy(t, each.handle, call, each.args)
		if !strings.Contains(said, "mnemo run") {
			t.Errorf("%s refuses with %q, which does not say how to get a name", each.tool, said)
		}
	}

	// Peers is the exception, and it has to be: it is how an agent finds out
	// that it has no name and who else is there.
	blocks, _ := structured(t, handlePeers, call, Args{})
	if !strings.Contains(blocks[len(blocks)-1], "mnemo run <tool> --name") {
		t.Errorf("peers does not say how to get a name: %q", blocks[len(blocks)-1])
	}
}

// When another live chat holds the name, this one may look but not act. Sending
// under a name whose answers go elsewhere is worse than being stopped.
func TestAChatWhoseNameWasTakenIsToldInEveryAnswer(t *testing.T) {
	calls := chats(t, "alex", "alex")
	first, second := calls[0], calls[1]
	if _, err := first.box.Claim(t.Context(), first.Caller()); err != nil {
		t.Fatal(err)
	}
	_, err := second.box.Claim(t.Context(), second.Caller())
	if err == nil {
		t.Fatal("the second chat was given a name the first one holds")
	}
	second.ClaimRefusal = err.Error()

	for _, each := range []struct {
		tool   string
		handle Handler
		args   Args
	}{
		{"mailbox_send", handleSend, Args{"to": "somebody@here", "body": "hello"}},
		{"mailbox_reply", handleReply, Args{"id": "m1", "body": "hello"}},
		{"mailbox_read", handleRead, Args{}},
		{"mailbox_wait", handleWait, Args{"timeout_ms": 1}},
	} {
		said := refusedBy(t, each.handle, second, each.args)
		if said != second.ClaimRefusal {
			t.Errorf("%s refuses with %q, not with the reason the name was refused", each.tool, said)
		}
	}

	// Peers still answers, because it is how a user finds out which chat has the
	// name — and its identity line carries the warning.
	blocks, _ := structured(t, handlePeers, second, Args{})
	identity := blocks[len(blocks)-1]
	if !strings.HasPrefix(identity, "You are alex@here (claude).") {
		t.Errorf("the identity line is %q", identity)
	}
	if !strings.Contains(identity, "warning: "+second.ClaimRefusal) {
		t.Errorf("the identity line does not carry the refusal: %q", identity)
	}
}

// Peers answers the question "who can I reach right now", so live has to mean
// confirmed now, and the two words have to be explained: offline reads as
// unreachable and it is not.
func TestPeersSaysWhoIsThereAndWhatTheWordsMean(t *testing.T) {
	calls := chats(t, "alex", "worker-api")
	alex, worker := calls[0], calls[1]
	if _, err := worker.box.Claim(t.Context(), worker.Caller()); err != nil {
		t.Fatal(err)
	}
	structured(t, handleSend, worker, Args{"to": "alex@here", "body": "status?"})

	blocks, out := structured(t, handlePeers, alex, Args{})
	if blocks[0] != "Mailbox on here." {
		t.Errorf("the first block is %q", blocks[0])
	}
	if !strings.Contains(blocks[1], "| alex@here (you) | claude | offline | 1 |") {
		t.Errorf("the caller's row reads wrong:\n%s", blocks[1])
	}
	if !strings.Contains(blocks[1], "| worker-api@here | claude | live | 0 |") {
		t.Errorf("the holder's row reads wrong:\n%s", blocks[1])
	}
	if !strings.Contains(blocks[2], "messages to it wait") {
		t.Errorf("offline is not explained: %q", blocks[2])
	}
	if out["mailbox_machine"] != "here" || out["you"] != "alex@here" {
		t.Errorf("the structured answer says %v", out)
	}
	rows, _ := out["peers"].([]any)
	if len(rows) != 2 {
		t.Fatalf("the structured answer carries %v", out["peers"])
	}
	// The caller comes first, which is what makes "and to see who you are" true.
	caller, _ := rows[0].(map[string]any)
	if caller["address"] != "alex@here" || caller["you"] != true || caller["unread"] != float64(1) {
		t.Errorf("the first row is %v", caller)
	}
}

// Waiting is how an agent that asked something blocks for the answer. A timeout
// is a normal answer, not a failure, and it has to say how to keep waiting.
func TestWaitingThatFindsNothingSaysSoWithoutFailing(t *testing.T) {
	call := chats(t, "alex")[0]

	blocks, out := structured(t, handleWait, call, Args{"timeout_ms": 1})
	if !strings.Contains(blocks[0], "Call mailbox_wait again to keep waiting.") {
		t.Errorf("a timeout reads %q", blocks[0])
	}
	if out["timed_out"] != true {
		t.Errorf("the structured answer does not say it timed out: %v", out)
	}
	if got, ok := out["messages"].([]any); !ok || len(got) != 0 {
		// An empty list, not null: a client iterates it, and null is a crash
		// where empty is an answer.
		t.Errorf("messages came back as %v", out["messages"])
	}
}

// A refusal is one block and carries nothing to parse. A terminal command that
// found structured content on a refusal would report a message it never sent.
func TestARefusalCarriesNothingToParse(t *testing.T) {
	call := chats(t, "alex")[0]

	answered := handleSend(t.Context(), call, Args{"to": "alex@here", "body": "talking to myself"})
	if !answered.result().IsError {
		t.Fatal("a message to itself was accepted")
	}
	if answered.structured != nil {
		t.Errorf("a refusal carries structured content: %v", answered.structured)
	}
}

// A chat that did not say which tool it is still gets told who it is, from the
// name the client gave itself in the handshake.
//
// It is shown and never recorded, which is the whole point: a client chooses
// that name, and a label it chose must not be able to make `@claude` reach a
// chat that is not Claude Code.
func TestAClientThatDidNotSayWhichToolItIsIsStillNamed(t *testing.T) {
	for name, want := range map[string]string{
		"Some Editor":   "some-editor",
		"  VS/Code 2 ":  "vs-code-2",
		"claude-ish!!!": "claude-ish",
		"!!!":           "",
	} {
		if got := label(name); got != want {
			t.Errorf("label(%q) = %q, want %q", name, got, want)
		}
	}

	call := chats(t, "alex")[0]
	call.Tool = ""
	call.Client = "some-editor"
	blocks, _ := structured(t, handlePeers, call, Args{})
	if identity := blocks[len(blocks)-1]; identity != "You are alex@here (some-editor)." {
		t.Errorf("the identity line is %q", identity)
	}

	// And the address keeps no tool at all, so no group reaches it by that name.
	peers, err := call.box.List(t.Context(), call.Caller())
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range peers.Peers {
		if peer.Address == "alex@here" && peer.Tool != "" {
			t.Errorf("the client's own label was recorded on the address as %q", peer.Tool)
		}
	}
}
