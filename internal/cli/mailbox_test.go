package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mailboxEnv is a machine whose mailbox is in a directory of its own, so a test
// can look at the files or put a holder in them.
func mailboxEnv(t *testing.T) (dir string, env []string) {
	t.Helper()
	_, env = machine(t, false)
	dir = filepath.Join(t.TempDir(), "mailbox")
	return dir, append(env, "MNEMO_MAILBOX_DIR="+dir)
}

// id is the message id in a line of output, which is the only way a test can
// reply to what it sent: the ids carry random characters by design.
func idIn(t *testing.T, out string) string {
	t.Helper()
	for _, field := range strings.Fields(out) {
		if strings.HasPrefix(field, "m") && len(field) > 3 && !strings.Contains(field, "@") {
			return field
		}
	}
	t.Fatalf("no message id in:\n%s", out)
	return ""
}

// A person hands out a task and reads the answer, without opening a chat. This
// is the whole of the mailbox from a terminal, in the order somebody uses it.
func TestTheTerminalCommandsCarryTheConversation(t *testing.T) {
	_, env := mailboxEnv(t)

	out, code := runMnemo(t, env, "peers")
	if code != 0 {
		t.Fatalf("peers: %d\n%s", code, out)
	}
	// Which mailbox you are looking at, always: there is otherwise no way to
	// tell a local one from a server's.
	if !strings.Contains(out, "mailbox on testbox") {
		t.Errorf("peers does not say where the mailbox is:\n%s", out)
	}
	if !strings.Contains(out, "no agents known yet") {
		t.Errorf("an empty mailbox reads:\n%s", out)
	}

	// A name nobody has used yet is still written to — the message waits — but
	// the warning is what tells somebody they typed it wrong.
	out, code = runMnemo(t, env, "send", "worker-api@testbox", "rebase before merging", "--name", "alex")
	if code != 0 {
		t.Fatalf("send: %d\n%s", code, out)
	}
	sent := idIn(t, out)
	if !strings.Contains(out, "sent "+sent+" to worker-api@testbox as alex@testbox") {
		t.Errorf("the send reads:\n%s", out)
	}
	if !strings.Contains(out, "warning: ") {
		t.Errorf("sending to a name nobody has used says nothing about it:\n%s", out)
	}

	out, code = runMnemo(t, env, "peers", "--name", "alex")
	if code != 0 {
		t.Fatalf("peers: %d\n%s", code, out)
	}
	for _, want := range []string{"ADDRESS", "alex@testbox", "terminal", "(you)"} {
		if !strings.Contains(out, want) {
			t.Errorf("peers does not show %q:\n%s", want, out)
		}
	}

	out, code = runMnemo(t, env, "read", "--name", "worker-api")
	if code != 0 {
		t.Fatalf("read: %d\n%s", code, out)
	}
	if !strings.Contains(out, sent) || !strings.Contains(out, "from alex@testbox (terminal)") {
		t.Errorf("the message reads:\n%s", out)
	}
	if !strings.Contains(out, "rebase before merging") {
		t.Errorf("the body is missing:\n%s", out)
	}

	// Read means read. A second call must not hand the same message over again.
	out, code = runMnemo(t, env, "read", "--name", "worker-api")
	if code != 0 {
		t.Fatalf("read: %d\n%s", code, out)
	}
	if !strings.Contains(out, "no new messages for worker-api@testbox") {
		t.Errorf("the message came back a second time:\n%s", out)
	}

	out, code = runMnemo(t, env, "reply", sent, "rebased, CI green", "--name", "worker-api")
	if code != 0 {
		t.Fatalf("reply: %d\n%s", code, out)
	}
	if !strings.Contains(out, "in reply to "+sent) || !strings.Contains(out, "to alex@testbox") {
		t.Errorf("the reply reads:\n%s", out)
	}

	out, code = runMnemo(t, env, "read", "--name", "alex")
	if code != 0 {
		t.Fatalf("read: %d\n%s", code, out)
	}
	if !strings.Contains(out, "in reply to "+sent) || !strings.Contains(out, "rebased, CI green") {
		t.Errorf("the answer that came back reads:\n%s", out)
	}
}

// A refusal reaches a person in a shell, and the mailbox writes it with the
// names of the MCP tools in it. Telling somebody to check the spelling with
// mailbox_peers is telling them to do something they cannot do.
func TestARefusalFromATerminalNamesCommandsAndNotTools(t *testing.T) {
	_, env := mailboxEnv(t)

	// A bare name nobody has used: the refusal points at the list of names.
	out, code := runMnemo(t, env, "send", "nobody-here", "hello", "--name", "alex")
	if code != 1 {
		t.Fatalf("it exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "mnemo peers") || strings.Contains(out, "mailbox_peers") {
		t.Errorf("the refusal reads:\n%s", out)
	}

	// Listing projects is the one thing a person has no command for, so the
	// refusal points at the directory instead of at a tool they cannot call.
	out, code = runMnemo(t, env, "send", "alex@testbox", "hello", "--name", "worker",
		"--project", "not-a-project")
	if code != 1 {
		t.Fatalf("it exited %d:\n%s", code, out)
	}
	if strings.Contains(out, "mnemo_list_projects") || !strings.Contains(out, "ls ") {
		t.Errorf("the refusal reads:\n%s", out)
	}

	// And the same for a warning on a send that went through, which is the one
	// a person sees most: it is what tells them they typed the name wrong.
	out, code = runMnemo(t, env, "send", "typo@testbox", "hello", "--name", "alex")
	if code != 0 {
		t.Fatalf("it exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "mnemo peers") || strings.Contains(out, "mailbox_peers") {
		t.Errorf("the warning reads:\n%s", out)
	}
}

// A terminal command may not use the name of an open chat. Reading its post
// would mark it read, and the chat would never see what was sent to it.
func TestATerminalCommandWillNotTakeAnOpenChatsName(t *testing.T) {
	dir, env := mailboxEnv(t)

	// A holder whose process really is alive — this test — because that is what
	// liveness is judged by for a local holder. The instance is somebody else's.
	since := time.Now().UTC().Format(time.RFC3339Nano)
	write(t, filepath.Join(dir, "state.json"), fmt.Sprintf(`{"version":2,"next_seq":1,"addresses":{
		"worker-api@testbox":{"tool":"claude","floor":0,"cursor":0,"last_seen":%q,
		"holder":{"instance":"another-chat","via":"local","pid":%d,"since":%q}}}}`,
		since, os.Getpid(), since))

	for _, args := range [][]string{
		{"read", "--name", "worker-api"},
		{"send", "alex@testbox", "hello", "--name", "worker-api"},
		{"reply", "m1aaaa", "hello", "--name", "worker-api"},
	} {
		out, code := runMnemo(t, env, args...)
		if code != 1 {
			t.Errorf("mnemo %v exited %d:\n%s", args, code, out)
		}
		if !strings.Contains(out, "worker-api@testbox is already open in claude") {
			t.Errorf("mnemo %v does not say who has the name:\n%s", args, out)
		}
	}

	// Looking is not holding. peers is how somebody finds out which chat has the
	// name, so refusing it would refuse the answer to the question it raises.
	out, code := runMnemo(t, env, "peers", "--name", "worker-api")
	if code != 0 {
		t.Fatalf("peers was refused: %d\n%s", code, out)
	}
	if !strings.Contains(out, "worker-api@testbox") || !strings.Contains(out, "live") {
		t.Errorf("peers does not show the chat that holds the name:\n%s", out)
	}

	// And the command gave the name back: a command that kept it would stop a
	// chat opening under it, with no process left to notice.
	out, code = runMnemo(t, env, "read", "--name", "alex")
	if code != 0 {
		t.Fatalf("read as another name: %d\n%s", code, out)
	}
	state := read(t, filepath.Join(dir, "state.json"))
	if strings.Count(state, `"holder"`) != 1 {
		t.Errorf("a finished command left its name held:\n%s", state)
	}
}

// A name is never guessed. Sending under one whose answers go somewhere this
// person is not looking is worse than being told to say which name.
func TestAMailboxCommandWithNoNameStopsRatherThanGuessing(t *testing.T) {
	_, env := mailboxEnv(t)

	for _, args := range [][]string{
		{"read"},
		{"send", "somebody@testbox", "hello"},
		{"reply", "m1aaaa", "hello"},
	} {
		out, code := runMnemo(t, env, args...)
		// Two, not one: a script has to be able to tell "you typed it wrong"
		// from "it did not work".
		if code != 2 {
			t.Errorf("mnemo %v exited %d:\n%s", args, code, out)
		}
		if !strings.Contains(out, "--name") || !strings.Contains(out, "MNEMO_AGENT") {
			t.Errorf("mnemo %v does not say how to give a name:\n%s", args, out)
		}
	}

	// peers is the exception: it answers without a name, because "who is there"
	// is a question somebody asks before they have one.
	if out, code := runMnemo(t, env, "peers"); code != 0 {
		t.Errorf("peers needed a name: %d\n%s", code, out)
	}

	// The name can come from the environment, which is what a chat's own shell
	// has set.
	out, code := runMnemo(t, append(env, "MNEMO_AGENT=alex"), "read")
	if code != 0 {
		t.Fatalf("read with MNEMO_AGENT: %d\n%s", code, out)
	}
	if !strings.Contains(out, "alex@testbox") {
		t.Errorf("the name from the environment was not used:\n%s", out)
	}
}
