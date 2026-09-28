package mailbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexParco/mnemo/internal/lock"
)

// fixed is what Now returns, so an id and a timestamp are predictable.
var fixed = time.Date(2026, 3, 4, 14, 3, 12, 345_000_000, time.UTC)

// box is a mailbox on a machine called `here`, with a clock a test can move and
// a liveness check a test can answer for.
type box struct {
	*Mailbox
	now   time.Time
	alive map[int]bool
}

func newBox(t *testing.T, projects ...string) *box {
	t.Helper()
	base := t.TempDir()
	b := &box{now: fixed, alive: map[int]bool{}}
	b.Mailbox = New(filepath.Join(base, "mailbox"), "here", Options{
		Locker: lock.New(filepath.Join(base, "locks")),
		Now:    func() time.Time { return b.now },
		ProjectExists: func(slug string) bool {
			for _, known := range projects {
				if slug == known {
					return true
				}
			}
			return false
		},
		Alive: func(pid int) bool { return b.alive[pid] },
	})
	return b
}

// agent is a caller with a name, on this machine, whose process the test says is
// alive.
func (b *box) agent(t *testing.T, name, tool string, pid int) Caller {
	t.Helper()
	b.alive[pid] = true
	caller := Caller{Name: name, Machine: "here", Tool: tool, Instance: Instance(), Via: "local", PID: pid}
	if _, err := b.Claim(t.Context(), caller); err != nil {
		t.Fatalf("claiming %s: %v", name, err)
	}
	return caller
}

func (b *box) send(t *testing.T, from Caller, to, body string) Message {
	t.Helper()
	sent, err := b.Send(t.Context(), from, to, body, "")
	if err != nil {
		t.Fatalf("sending to %s: %v", to, err)
	}
	return sent.Message
}

func (b *box) read(t *testing.T, caller Caller) []Message {
	t.Helper()
	messages, err := b.Read(t.Context(), caller)
	if err != nil {
		t.Fatalf("reading for %s: %v", caller.Address(), err)
	}
	return messages
}

func bodies(messages []Message) string {
	var out []string
	for _, message := range messages {
		out = append(out, message.Body)
	}
	return strings.Join(out, "|")
}

// An agent that was not opened with `mnemo run` has memory and no mailbox. That
// is the whole of the admission control, so it is the first thing tested.
func TestAnAgentWithNoNameHasNoMailbox(t *testing.T) {
	b := newBox(t)
	nameless := Caller{Machine: "here", Tool: "claude", Instance: Instance(), Via: "local", PID: 1}

	if _, err := b.Send(t.Context(), nameless, "other@here", "hello", ""); !IsRefusal(err) {
		t.Errorf("sending gave %v, want a refusal", err)
	} else if !strings.Contains(err.Error(), "mnemo run") {
		t.Errorf("the refusal does not say how to get one: %v", err)
	}
	if _, err := b.Read(t.Context(), nameless); !IsRefusal(err) {
		t.Errorf("reading gave %v", err)
	}
	if _, err := b.Claim(t.Context(), nameless); !IsRefusal(err) {
		t.Errorf("claiming gave %v", err)
	}
}

// One address, one live instance. Without that a reply has nowhere definite to go.
func TestOneNameOneChat(t *testing.T) {
	b := newBox(t)
	first := b.agent(t, "worker", "claude", 100)

	second := Caller{Name: "worker", Machine: "here", Tool: "codex", Instance: Instance(), Via: "local", PID: 200}
	b.alive[200] = true
	_, err := b.Claim(t.Context(), second)
	if !IsRefusal(err) {
		t.Fatalf("a second chat took the name: %v", err)
	}
	for _, want := range []string{"worker@here", "claude", "14:03", "pick another name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}

	// The same instance claiming again is a renewal, not a collision.
	if _, err := b.Claim(t.Context(), first); err != nil {
		t.Errorf("renewing: %v", err)
	}

	// Once the first chat is gone, the name is free — and the claim says when the
	// new chat opened, not when the old one did.
	b.alive[100] = false
	b.now = b.now.Add(time.Hour)
	held, err := b.Claim(t.Context(), second)
	if err != nil {
		t.Fatalf("claiming a freed name: %v", err)
	}
	if !held.Since.Equal(b.now) {
		t.Errorf("since = %s, want the new chat's own start", held.Since)
	}
}

// A remote holder has no process to check, so it lives on a lease that its
// heartbeat renews. A client that vanishes has to lose the name.
func TestARemoteHolderLivesOnItsLease(t *testing.T) {
	b := newBox(t)
	remote := Caller{Name: "worker", Machine: "laptop", Tool: "claude", Instance: Instance(), Via: "remote"}

	held, err := b.Claim(t.Context(), remote)
	if err != nil {
		t.Fatal(err)
	}
	if !held.LeaseUntil.Equal(fixed.Add(Lease)) {
		t.Errorf("lease ends at %s, want %s", held.LeaseUntil, fixed.Add(Lease))
	}

	// Still held while the lease lasts.
	b.now = fixed.Add(Lease - time.Second)
	other := Caller{Name: "worker", Machine: "laptop", Tool: "codex", Instance: Instance(), Via: "remote"}
	if _, err := b.Claim(t.Context(), other); !IsRefusal(err) {
		t.Errorf("the name was taken while the lease held: %v", err)
	}

	// And free once it runs out.
	b.now = fixed.Add(Lease + time.Second)
	if _, err := b.Claim(t.Context(), other); err != nil {
		t.Errorf("the lease did not expire: %v", err)
	}
}

func TestSendAndRead(t *testing.T) {
	b := newBox(t)
	sender := b.agent(t, "worker", "claude", 100)
	receiver := b.agent(t, "boss", "codex", 200)

	sent, err := b.Send(t.Context(), sender, "boss@here", "  the parser is done  ", "")
	if err != nil {
		t.Fatal(err)
	}
	message := sent.Message
	if message.Body != "the parser is done" {
		t.Errorf("the body was not trimmed: %q", message.Body)
	}
	if message.Seq != 1 || message.From != "worker@here" || message.To != "boss@here" {
		t.Errorf("message = %+v", message)
	}
	if message.TS != "2026-03-04T14:03:12.345Z" {
		t.Errorf("the timestamp is %q", message.TS)
	}
	if !strings.HasPrefix(message.ID, "m1") {
		t.Errorf("the id is %q, want m + seq in base 36 + four hex", message.ID)
	}

	got := b.read(t, receiver)
	if bodies(got) != "the parser is done" {
		t.Errorf("read returned %q", bodies(got))
	}
	// Read once.
	if again := b.read(t, receiver); len(again) != 0 {
		t.Errorf("reading twice returned %q", bodies(again))
	}
	// And never to the sender.
	if own := b.read(t, sender); len(own) != 0 {
		t.Errorf("the sender read its own message: %q", bodies(own))
	}
}

// A cursor belongs to the address, not to the process, so a chat that restarts
// under the same name carries on where it left off rather than re-reading
// everything.
func TestACursorSurvivesTheChatThatOwnedIt(t *testing.T) {
	b := newBox(t)
	sender := b.agent(t, "worker", "claude", 100)
	first := b.agent(t, "boss", "codex", 200)

	b.send(t, sender, "boss@here", "one")
	if got := b.read(t, first); bodies(got) != "one" {
		t.Fatalf("read %q", bodies(got))
	}

	// The chat closes and another opens with the same name.
	b.alive[200] = false
	restarted := b.agent(t, "boss", "codex", 300)
	b.send(t, sender, "boss@here", "two")

	got := b.read(t, restarted)
	if bodies(got) != "two" {
		t.Errorf("a restarted chat read %q, want only what arrived since", bodies(got))
	}
}

// A group reaches the agents that existed when it was sent. An agent opened
// afterwards is not answerable for a conversation it was never part of — but a
// direct message sent before it appeared does wait for it.
func TestGroupsReachWhoWasThereAndDirectMessagesWait(t *testing.T) {
	b := newBox(t)
	sender := b.agent(t, "worker", "claude", 100)
	early := b.agent(t, "early", "claude", 200)

	b.send(t, sender, "@all", "to everyone who is here")
	b.send(t, sender, "late@here", "waiting for you")

	late := b.agent(t, "late", "codex", 300)

	if got := b.read(t, early); bodies(got) != "to everyone who is here" {
		t.Errorf("the agent that was there read %q", bodies(got))
	}
	if got := b.read(t, late); bodies(got) != "waiting for you" {
		t.Errorf("the agent that arrived later read %q, want only its own message", bodies(got))
	}

	// A group sent now does reach it.
	b.send(t, sender, "@all", "and now everyone")
	if got := b.read(t, late); bodies(got) != "and now everyone" {
		t.Errorf("read %q", bodies(got))
	}
}

func TestToolGroups(t *testing.T) {
	b := newBox(t)
	sender := b.agent(t, "worker", "opencode", 100)
	inClaude := b.agent(t, "one", "claude", 200)
	inCodex := b.agent(t, "two", "codex", 300)

	b.send(t, sender, "@claude", "for the claude chats")

	if got := b.read(t, inClaude); bodies(got) != "for the claude chats" {
		t.Errorf("the claude chat read %q", bodies(got))
	}
	if got := b.read(t, inCodex); len(got) != 0 {
		t.Errorf("the codex chat read %q", bodies(got))
	}

	// A tool label that is not one of the known ones is display only, so a group
	// address never matches it.
	derived := b.agent(t, "three", "some-editor", 400)
	b.send(t, sender, "@all", "everyone")
	if got := b.read(t, derived); bodies(got) != "everyone" {
		t.Errorf("@all did not reach a derived tool label: %q", bodies(got))
	}
}

func TestSendRefusals(t *testing.T) {
	b := newBox(t, "shop")
	sender := b.agent(t, "worker", "claude", 100)
	b.agent(t, "boss", "codex", 200)

	cases := []struct{ name, to, body, project, says string }{
		{"an empty body", "boss@here", "   ", "", "empty"},
		{"a group that is not one", "@everybody", "hello", "", "not a group"},
		{"yourself", "worker@here", "hello", "", "That is you"},
		{"a name nobody has used", "ghost", "hello", "", "has appeared yet"},
		{"a project that does not exist", "boss@here", "hello", "nowhere", "No project 'nowhere'"},
		{"a project that is not a slug", "boss@here", "hello", "Not A Slug", "kebab-case"},
		{"nobody at all", "", "hello", "", "Say who this is for"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := b.Send(t.Context(), sender, c.to, c.body, c.project)
			if !IsRefusal(err) {
				t.Fatalf("got %v, want a refusal", err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not say %q: %v", c.says, err)
			}
		})
	}

	t.Run("a body that is too long", func(t *testing.T) {
		_, err := b.Send(t.Context(), sender, "boss@here", strings.Repeat("x", MaxBody+1), "")
		if !IsRefusal(err) {
			t.Fatalf("got %v", err)
		}
		if !strings.Contains(err.Error(), "send the path") {
			t.Errorf("the refusal does not say what to do instead: %v", err)
		}
	})

	t.Run("an address nobody has used is a warning, not a refusal", func(t *testing.T) {
		sent, err := b.Send(t.Context(), sender, "nobody@elsewhere", "hello", "")
		if err != nil {
			t.Fatalf("sending to an unused address: %v", err)
		}
		if !strings.Contains(sent.Warning, "will wait") {
			t.Errorf("the warning reads %q", sent.Warning)
		}
	})
}

// A bare name is the convenient form and the ambiguous one.
func TestABareNameMustBeUnambiguous(t *testing.T) {
	b := newBox(t)
	sender := b.agent(t, "worker", "claude", 100)

	here := Caller{Name: "boss", Machine: "here", Tool: "codex", Instance: Instance(), Via: "local", PID: 200}
	b.alive[200] = true
	if _, err := b.Claim(t.Context(), here); err != nil {
		t.Fatal(err)
	}
	if sent, err := b.Send(t.Context(), sender, "boss", "one"+"", ""); err != nil {
		t.Fatalf("one match: %v", err)
	} else if sent.Message.To != "boss@here" {
		t.Errorf("resolved to %q", sent.Message.To)
	}

	// The same name on another machine makes it ambiguous, and the refusal has to
	// list both so the agent can choose.
	elsewhere := Caller{Name: "boss", Machine: "laptop", Tool: "claude", Instance: Instance(), Via: "remote"}
	if _, err := b.Claim(t.Context(), elsewhere); err != nil {
		t.Fatal(err)
	}
	_, err := b.Send(t.Context(), sender, "boss", "two", "")
	if !IsRefusal(err) {
		t.Fatalf("an ambiguous name was accepted: %v", err)
	}
	for _, want := range []string{"boss@here", "boss@laptop"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not list %q: %v", want, err)
		}
	}
}

func TestReply(t *testing.T) {
	b := newBox(t, "shop")
	sender := b.agent(t, "worker", "claude", 100)
	receiver := b.agent(t, "boss", "codex", 200)

	original, err := b.Send(t.Context(), sender, "boss@here", "the parser is done", "shop")
	if err != nil {
		t.Fatal(err)
	}
	b.read(t, receiver)

	reply, err := b.Reply(t.Context(), receiver, original.Message.ID, "good, ship it")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Message.To != "worker@here" || reply.Message.ReplyTo != original.Message.ID {
		t.Errorf("reply = %+v", reply.Message)
	}
	if reply.Message.Project != "shop" {
		t.Errorf("the reply did not inherit the project: %q", reply.Message.Project)
	}
	if got := b.read(t, sender); bodies(got) != "good, ship it" {
		t.Errorf("the sender read %q", bodies(got))
	}

	// More than one reply is allowed: a conversation is not a single exchange.
	if _, err := b.Reply(t.Context(), receiver, original.Message.ID, "and one more thing"); err != nil {
		t.Errorf("a second reply: %v", err)
	}

	t.Run("a message that was not yours", func(t *testing.T) {
		other := b.agent(t, "bystander", "claude", 300)
		_, err := b.Reply(t.Context(), other, original.Message.ID, "butting in")
		if !IsRefusal(err) {
			t.Fatalf("got %v", err)
		}
		if !strings.Contains(err.Error(), "not yours to answer") {
			t.Errorf("the refusal reads %v", err)
		}
	})

	t.Run("a message that is not there", func(t *testing.T) {
		_, err := b.Reply(t.Context(), receiver, "mnothing", "hello")
		if !IsRefusal(err) {
			t.Fatalf("got %v", err)
		}
		if !strings.Contains(err.Error(), "30 days") {
			t.Errorf("the refusal does not say how long they are kept: %v", err)
		}
	})

	t.Run("a group message can be answered by anyone it reached", func(t *testing.T) {
		group := b.send(t, sender, "@all", "anyone know about the schema?")
		b.read(t, receiver)
		answer, err := b.Reply(t.Context(), receiver, group.ID, "I do")
		if err != nil {
			t.Fatalf("replying to a group message: %v", err)
		}
		if answer.Message.To != "worker@here" {
			t.Errorf("the reply went to %q, want whoever sent it", answer.Message.To)
		}
	})
}

func TestPeers(t *testing.T) {
	b := newBox(t)
	caller := b.agent(t, "worker", "claude", 100)
	other := b.agent(t, "boss", "codex", 200)
	b.send(t, caller, "boss@here", "one")
	b.send(t, caller, "boss@here", "two")

	peers, err := b.List(t.Context(), caller)
	if err != nil {
		t.Fatal(err)
	}
	if peers.Machine != "here" {
		t.Errorf("the mailbox says it is on %q", peers.Machine)
	}
	if len(peers.Peers) != 2 {
		t.Fatalf("peers = %+v", peers.Peers)
	}
	// The caller first: the question this answers is "who can I reach".
	if !peers.Peers[0].Caller || peers.Peers[0].Address != "worker@here" {
		t.Errorf("the first row is %+v", peers.Peers[0])
	}
	if peers.Peers[1].Address != "boss@here" || peers.Peers[1].Unread != 2 || !peers.Peers[1].Live {
		t.Errorf("the second row is %+v", peers.Peers[1])
	}

	// A closed chat is offline, and that is checked rather than remembered.
	b.alive[200] = false
	peers, err = b.List(t.Context(), caller)
	if err != nil {
		t.Fatal(err)
	}
	if peers.Peers[1].Live {
		t.Error("a closed chat is still reported live")
	}
	if peers.Peers[1].Unread != 2 {
		t.Errorf("a closed chat lost its unread count: %+v", peers.Peers[1])
	}
	_ = other
}

// An address with nothing waiting is forgotten after long enough. One with unread
// messages never is: that is somebody who has not come back yet.
func TestAnAddressWithNothingWaitingIsForgotten(t *testing.T) {
	b := newBox(t)
	caller := b.agent(t, "worker", "claude", 100)
	gone := b.agent(t, "gone", "codex", 200)
	waiting := b.agent(t, "waiting", "codex", 300)

	b.send(t, caller, "gone@here", "read this")
	b.read(t, gone)
	b.send(t, caller, "waiting@here", "never read")

	b.alive[200], b.alive[300] = false, false
	b.now = b.now.Add(Offline + time.Hour)

	peers, err := b.List(t.Context(), caller)
	if err != nil {
		t.Fatal(err)
	}
	var addresses []string
	for _, peer := range peers.Peers {
		addresses = append(addresses, peer.Address)
	}
	joined := strings.Join(addresses, ",")
	if strings.Contains(joined, "gone@here") {
		t.Errorf("an address with nothing waiting was kept: %s", joined)
	}
	if !strings.Contains(joined, "waiting@here") {
		t.Errorf("an address with unread messages was forgotten: %s", joined)
	}
	_ = waiting
}

// A watcher polls without the lock, so it must never block an agent and never
// mark anything read.
func TestPeekChangesNothing(t *testing.T) {
	b := newBox(t)
	sender := b.agent(t, "worker", "claude", 100)
	receiver := b.agent(t, "boss", "codex", 200)
	b.send(t, sender, "boss@here", "one")

	top, waiting, err := b.Peek("boss@here", 0)
	if err != nil {
		t.Fatal(err)
	}
	if top != 1 || bodies(waiting) != "one" {
		t.Errorf("peek returned %d, %q", top, bodies(waiting))
	}

	// Nothing was marked read.
	if got := b.read(t, receiver); bodies(got) != "one" {
		t.Errorf("peek marked it read: %q", bodies(got))
	}

	// An address nobody has held gets no group messages: with no floor, every
	// group message would look new for ever.
	b.send(t, sender, "@all", "everyone")
	if _, waiting, err := b.Peek("stranger@here", 0); err != nil {
		t.Fatal(err)
	} else if len(waiting) != 0 {
		t.Errorf("an unknown address peeked %q", bodies(waiting))
	}
}

// The log is append-only and a crash can tear the last line. Losing that one
// message is the cost of never losing the rest.
func TestATornLineIsSkippedAndTheNextMessageSurvives(t *testing.T) {
	b := newBox(t)
	sender := b.agent(t, "worker", "claude", 100)
	receiver := b.agent(t, "boss", "codex", 200)
	b.send(t, sender, "boss@here", "first")

	// A half-written line, with no newline, as a crash mid-append leaves it.
	file, err := os.OpenFile(b.logPath(), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"seq":2,"id":"m2","body":"tor`); err != nil {
		t.Fatal(err)
	}
	file.Close()

	b.send(t, sender, "boss@here", "after the tear")

	got := b.read(t, receiver)
	if bodies(got) != "first|after the tear" {
		t.Errorf("read %q, want the torn line skipped and both real messages kept", bodies(got))
	}
}

// After a crash the counter can be behind what was appended. Reusing a seq would
// make one message shadow another in every cursor.
func TestSeqIsNeverReused(t *testing.T) {
	b := newBox(t)
	sender := b.agent(t, "worker", "claude", 100)
	receiver := b.agent(t, "boss", "codex", 200)
	b.send(t, sender, "boss@here", "one")

	// The counter goes backwards, as a lost state write would leave it.
	state, err := b.loadState()
	if err != nil {
		t.Fatal(err)
	}
	state.NextSeq = 1
	if err := b.saveState(state); err != nil {
		t.Fatal(err)
	}

	second := b.send(t, sender, "boss@here", "two")
	if second.Seq != 2 {
		t.Errorf("the second message took seq %d, want 2", second.Seq)
	}
	if got := b.read(t, receiver); bodies(got) != "one|two" {
		t.Errorf("read %q", bodies(got))
	}
}

// A prune must never drop a message nobody has read, however old: the agent it
// was for may simply not have been opened since.
func TestPruningKeepsWhatNobodyHasRead(t *testing.T) {
	b := newBox(t)
	sender := b.agent(t, "worker", "claude", 100)
	readIt := b.agent(t, "reader", "codex", 200)
	b.agent(t, "silent", "codex", 300)

	b.send(t, sender, "reader@here", "read this")
	b.send(t, sender, "silent@here", "never read")
	b.send(t, sender, "@all", "to everyone")
	b.read(t, readIt)

	// Far enough ahead that everything is past its retention, and a prune is due.
	b.now = b.now.Add(Retention + 48*time.Hour)
	if _, err := b.List(t.Context(), sender); err != nil {
		t.Fatal(err)
	}

	messages, err := b.readLog()
	if err != nil {
		t.Fatal(err)
	}
	kept := bodies(messages)
	if strings.Contains(kept, "read this") {
		t.Errorf("a read message was kept: %q", kept)
	}
	if !strings.Contains(kept, "never read") {
		t.Errorf("a message nobody has read was pruned: %q", kept)
	}
	if strings.Contains(kept, "to everyone") {
		t.Errorf("an old group message was kept: %q", kept)
	}
}

// A prune that ran in the same operation as a send used to rewrite the log from
// the list as it was before the append, losing the message that was just sent.
func TestAPruneDoesNotSwallowTheMessageBeingSent(t *testing.T) {
	b := newBox(t)
	sender := b.agent(t, "worker", "claude", 100)
	receiver := b.agent(t, "boss", "codex", 200)

	b.send(t, sender, "boss@here", "old and read")
	b.read(t, receiver)
	b.now = b.now.Add(Retention + 48*time.Hour)

	fresh := b.send(t, sender, "boss@here", "sent as the prune ran")

	messages, err := b.readLog()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodies(messages), "sent as the prune ran") {
		t.Fatalf("the message sent during the prune is gone: %q", bodies(messages))
	}
	if got := b.read(t, receiver); bodies(got) != "sent as the prune ran" {
		t.Errorf("the receiver read %q", bodies(got))
	}
	_ = fresh
}

// Files from an earlier install are moved aside rather than read: the address
// format is not the one they use, and guessing at old addresses would cost a
// wrong reply.
func TestFilesFromAnEarlierInstallAreMovedAside(t *testing.T) {
	b := newBox(t)
	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.statePath(), []byte(`{"next_seq":9,"sessions":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.logPath(), []byte(`{"seq":1,"to":"session-abc","body":"from before"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	caller := b.agent(t, "worker", "claude", 100)
	peers, err := b.List(t.Context(), caller)
	if err != nil {
		t.Fatal(err)
	}
	if peers.Migrated == "" {
		t.Error("nothing said the old files were moved")
	}
	if _, err := os.Stat(filepath.Join(peers.Migrated, "state.json")); err != nil {
		t.Errorf("the old state was not kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(peers.Migrated, "messages.jsonl")); err != nil {
		t.Errorf("the old log was not kept: %v", err)
	}

	// And the mailbox starts empty rather than carrying addresses of a shape it
	// cannot resolve.
	messages, err := b.readLog()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bodies(messages), "from before") {
		t.Error("a message from the earlier install was read as if it were current")
	}
}

func TestWait(t *testing.T) {
	b := newBox(t)
	sender := b.agent(t, "worker", "claude", 100)
	receiver := b.agent(t, "boss", "codex", 200)
	// A real clock, because waiting is about time passing.
	b.Mailbox = New(b.Dir, "here", Options{
		Locker:        lock.New(filepath.Join(filepath.Dir(b.Dir), "locks")),
		ProjectExists: func(string) bool { return false },
		Alive:         func(pid int) bool { return b.alive[pid] },
	})

	t.Run("something already waiting comes back at once", func(t *testing.T) {
		b.send(t, sender, "boss@here", "already here")
		waited, err := b.Wait(t.Context(), receiver, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if waited.TimedOut || bodies(waited.Messages) != "already here" {
			t.Errorf("waited = %+v", waited)
		}
	})

	t.Run("nothing arriving times out and says so", func(t *testing.T) {
		waited, err := b.Wait(t.Context(), receiver, 400*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if !waited.TimedOut || len(waited.Messages) != 0 {
			t.Errorf("waited = %+v", waited)
		}
	})

	t.Run("a message that arrives during the wait", func(t *testing.T) {
		go func() {
			time.Sleep(150 * time.Millisecond)
			_, _ = b.Send(t.Context(), sender, "boss@here", "arrived late", "")
		}()
		waited, err := b.Wait(t.Context(), receiver, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if waited.TimedOut || bodies(waited.Messages) != "arrived late" {
			t.Errorf("waited = %+v", waited)
		}
	})

	t.Run("the ceiling is below the minute a client gives up at", func(t *testing.T) {
		if MaxWait >= time.Minute {
			t.Errorf("MaxWait is %s, which is not below the minute Codex cuts a call at", MaxWait)
		}
	})
}

// Releasing is a courtesy; the process check and the lease are the mechanism. But
// a clean exit must really free the name.
func TestReleasingFreesTheName(t *testing.T) {
	b := newBox(t)
	first := b.agent(t, "worker", "claude", 100)
	if err := b.Release(t.Context(), first); err != nil {
		t.Fatal(err)
	}

	second := Caller{Name: "worker", Machine: "here", Tool: "codex", Instance: Instance(), Via: "local", PID: 200}
	b.alive[200] = true
	if _, err := b.Claim(t.Context(), second); err != nil {
		t.Errorf("the name was not freed: %v", err)
	}

	// Another instance's release does not take a name away from its holder.
	stranger := Caller{Name: "worker", Machine: "here", Instance: Instance(), Via: "local", PID: 999}
	if err := b.Release(t.Context(), stranger); err != nil {
		t.Fatal(err)
	}
	if _, live, err := b.HeldBy(t.Context(), "worker@here"); err != nil {
		t.Fatal(err)
	} else if !live {
		t.Error("a stranger's release freed somebody else's name")
	}
}
