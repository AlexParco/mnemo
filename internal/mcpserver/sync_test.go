package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexParco/mnemo/internal/lock"
	"github.com/AlexParco/mnemo/internal/memory"
	"github.com/AlexParco/mnemo/internal/store"
)

// wired is a call whose store syncs through the given hub.
func wired(t *testing.T, remote string) *Call {
	t.Helper()
	call := writable(t, true)
	call.store = store.New(call.StoreDir, store.Options{
		Locker: lock.New(filepath.Join(filepath.Dir(call.StoreDir), "locks")),
		Remote: remote,
	})
	call.Remote = remote
	return call
}

// The whole flow, with two machines that really did collide: one hub, both
// change the same line of the same pending list, and the second one has to
// merge before it can publish.
func TestTwoMachinesCollideAndTheMergeGoesThrough(t *testing.T) {
	remote := hub(t)

	first := wired(t, remote)
	blocks(t, handleUpsertProject, first, Args{"slug": "shop", "name": "Shop"})
	blocks(t, handleWritePending, first, Args{"slug": "shop", "content": "## In progress\n\n- [ ] the parser\n"})
	blocks(t, handleCommit, first, Args{"message": "save(shop): from the first machine"})
	if got := blocks(t, handlePush, first, Args{}); !strings.Contains(got[0], "Pushed") {
		t.Fatalf("publishing: %q", got)
	}

	// The second machine starts from the hub.
	second := wired(t, remote)
	if got := blocks(t, handleBootstrap, second, Args{}); !strings.Contains(got[0], "Adopted") {
		t.Fatalf("the second machine did not adopt: %q", got)
	}

	// Both change the same line, and the first one gets there first.
	blocks(t, handleWritePending, first, Args{"slug": "shop", "content": "## In progress\n\n- [ ] the parser, done by monday\n"})
	blocks(t, handleCommit, first, Args{"message": "save(shop): a deadline"})
	blocks(t, handlePush, first, Args{})

	blocks(t, handleWritePending, second, Args{"slug": "shop", "content": "## In progress\n\n- [ ] the parser, blocked on the schema\n"})
	blocks(t, handleCommit, second, Args{"message": "save(shop): blocked"})

	// Syncing brings the collision back with the files to merge and the rule.
	conflicted := blocks(t, handleSync, second, Args{})
	if len(conflicted) != 3 {
		t.Fatalf("a sync with conflicts returned %d blocks: %q", len(conflicted), conflicted)
	}
	if !strings.Contains(conflicted[0], "1 conflicted file(s): projects/shop/pending.md") {
		t.Errorf("the first block reads:\n%s", conflicted[0])
	}
	if !strings.Contains(conflicted[1], "<<<<<<<") || !strings.Contains(conflicted[1], "blocked on the schema") {
		t.Errorf("the conflicted file did not come back with the answer:\n%s", conflicted[1])
	}
	if !strings.Contains(conflicted[2], "Keep the information from BOTH sides") {
		t.Errorf("the conflict rule is not the third block:\n%s", conflicted[2])
	}

	t.Run("a merge that still carries markers is refused", func(t *testing.T) {
		said := refusal(t, handleResolveConflict, second, Args{
			"file": "projects/shop/pending.md", "content": "## In progress\n\n<<<<<<< HEAD\n- [ ] a\n=======\n- [ ] b\n>>>>>>> them\n",
		})
		if !strings.Contains(said, "still contains conflict markers") {
			t.Errorf("the refusal reads:\n%s", said)
		}
	})

	t.Run("a path outside the store is refused", func(t *testing.T) {
		said := refusal(t, handleResolveConflict, second, Args{
			"file": "../escaped.md", "content": "anything\n",
		})
		if !strings.Contains(said, "not inside the store") {
			t.Errorf("the refusal reads:\n%s", said)
		}
	})

	// The union of both sides, which is what the rule asks for.
	merged := blocks(t, handleResolveConflict, second, Args{
		"file":    "projects/shop/pending.md",
		"content": "## In progress\n\n- [ ] the parser, done by monday, blocked on the schema\n",
	})
	if !strings.Contains(merged[0], "Nothing left conflicted") ||
		!strings.Contains(merged[0], "mnemo_rebase") {
		t.Errorf("resolving does not say what comes next: %q", merged[0])
	}

	finished := blocks(t, handleRebase, second, Args{"action": "continue"})
	if len(finished) == 0 || finished[0] == "" {
		t.Fatal("finishing the rebase said nothing")
	}
	if got := blocks(t, handlePush, second, Args{}); !strings.Contains(got[0], "Pushed") {
		t.Fatalf("the merged work was not published: %q", got)
	}

	// Both sides survived, on the hub, which is the whole point of the rule.
	third := wired(t, remote)
	blocks(t, handleBootstrap, third, Args{})
	pending := read(t, memory.PendingPath(third.StoreDir, "shop"))
	for _, want := range []string{"done by monday", "blocked on the schema"} {
		if !strings.Contains(pending, want) {
			t.Errorf("a third machine does not see %q:\n%s", want, pending)
		}
	}
}

// Continuing while something is still conflicted leaves the store mid-rebase.
// That must not read as success: it is the one state where an agent that
// wanders off leaves the memory half-merged.
func TestContinuingTooEarlyIsRefused(t *testing.T) {
	remote := hub(t)
	first := wired(t, remote)
	blocks(t, handleUpsertProject, first, Args{"slug": "shop", "name": "Shop"})
	blocks(t, handleWritePending, first, Args{"slug": "shop", "content": "## Next\n\n- [ ] one\n"})
	blocks(t, handleCommit, first, Args{"message": "save(shop): one"})
	blocks(t, handlePush, first, Args{})

	second := wired(t, remote)
	blocks(t, handleBootstrap, second, Args{})
	blocks(t, handleWritePending, first, Args{"slug": "shop", "content": "## Next\n\n- [ ] one, from here\n"})
	blocks(t, handleCommit, first, Args{"message": "save(shop): here"})
	blocks(t, handlePush, first, Args{})
	blocks(t, handleWritePending, second, Args{"slug": "shop", "content": "## Next\n\n- [ ] one, from there\n"})
	blocks(t, handleCommit, second, Args{"message": "save(shop): there"})
	blocks(t, handleSync, second, Args{})

	said := refusal(t, handleRebase, second, Args{"action": "continue"})
	if !strings.Contains(said, "projects/shop/pending.md") {
		t.Errorf("the refusal does not name what is unresolved:\n%s", said)
	}

	// Abort is the way out, and it puts the store back as it was.
	out := blocks(t, handleRebase, second, Args{"action": "abort"})
	if len(out) == 0 {
		t.Fatal("aborting said nothing")
	}
	if pending := read(t, memory.PendingPath(second.StoreDir, "shop")); !strings.Contains(pending, "from there") {
		t.Errorf("aborting did not put this machine's own work back:\n%s", pending)
	}
	if strings.Contains(read(t, memory.PendingPath(second.StoreDir, "shop")), "<<<<<<<") {
		t.Error("the store was left with conflict markers in it")
	}
}

// The scan before a push has no way around it, and the refusal carries what is
// needed to decide — never the value itself.
func TestPushRefusesASecretAndCanBeAcknowledged(t *testing.T) {
	remote := hub(t)
	call := wired(t, remote)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})
	secret := "ghp_" + strings.Repeat("B", 36)
	// Straight onto the disk: the write tools refuse this, and what is being
	// tested is the second line of defence, for a file a person edited by hand.
	path := memory.MemoryPath(call.StoreDir, "by-hand")
	put(t, path, "---\nid: by-hand\ntype: reference\nprojects: [shop]\nupdated: 2026-01-02\n---\n\nToken "+secret+"\n")
	blocks(t, handleCommit, call, Args{"message": "save(shop): edited by hand"})

	refused := blocks(t, handlePush, call, Args{})
	if len(refused) != 3 {
		t.Fatalf("a scan refusal returned %d blocks: %q", len(refused), refused)
	}
	if !strings.Contains(refused[0], "Nothing was pushed") {
		t.Errorf("the first block reads %q", refused[0])
	}
	if !strings.Contains(refused[1], "by-hand.md") {
		t.Errorf("the findings do not name the file:\n%s", refused[1])
	}
	for _, block := range refused {
		if strings.Contains(block, secret) {
			t.Fatal("the refusal repeated the secret back")
		}
	}

	token := acknowledgement(t, refused[2])
	if token == "" {
		t.Fatalf("the third block does not carry a value to pass back:\n%s", refused[2])
	}
	if got := blocks(t, handlePush, call, Args{"acknowledge": "not-the-one"}); !strings.Contains(got[0], "does not match") {
		t.Errorf("a wrong acknowledgement was accepted: %q", got)
	}
	if got := blocks(t, handlePush, call, Args{"acknowledge": token}); !strings.Contains(got[0], "Pushed") {
		t.Errorf("the acknowledged push did not go through: %q", got)
	}
}

// acknowledgement pulls the value out of the sentence that offers it, the way
// an agent would.
func acknowledgement(t *testing.T, block string) string {
	t.Helper()
	_, after, found := strings.Cut(block, "acknowledge: \"")
	if !found {
		return ""
	}
	token, _, _ := strings.Cut(after, "\"")
	return token
}

// A store with no hub cannot publish, and saying nothing would leave the agent
// believing the work had left the machine.
func TestPushingWithNoHub(t *testing.T) {
	call := writable(t, true)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})
	blocks(t, handleCommit, call, Args{"message": "save(shop): local"})

	said := refusal(t, handlePush, call, Args{})
	if !strings.Contains(strings.ToLower(said), "remote") {
		t.Errorf("the refusal reads:\n%s", said)
	}
}

// Nothing to publish is not something gone wrong.
func TestPushingWhenThereIsNothingToPush(t *testing.T) {
	remote := hub(t)
	call := wired(t, remote)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})
	blocks(t, handleCommit, call, Args{"message": "save(shop): one"})
	blocks(t, handlePush, call, Args{})

	got := blocks(t, handlePush, call, Args{})
	if len(got) != 1 {
		t.Fatalf("got %d blocks: %q", len(got), got)
	}
	if strings.Contains(got[0], "unexpectedly") {
		t.Errorf("nothing to do was reported as a failure: %q", got[0])
	}
}

// Syncing on a machine with no hub is an answer, not a crash.
func TestSyncingWithNoHub(t *testing.T) {
	call := writable(t, true)
	got := blocks(t, handleSync, call, Args{})
	if len(got) != 1 || got[0] == "" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got[0], "unexpectedly") {
		t.Errorf("a store with no hub reported a failure: %q", got[0])
	}
}

// conflicted leaves a store mid-rebase, the way an interrupted session does.
func conflicted(t *testing.T) *Call {
	t.Helper()
	remote := hub(t)
	first := wired(t, remote)
	blocks(t, handleUpsertProject, first, Args{"slug": "shop", "name": "Shop"})
	blocks(t, handleWritePending, first, Args{"slug": "shop", "content": "## Next\n\n- [ ] one\n"})
	blocks(t, handleCommit, first, Args{"message": "save(shop): one"})
	blocks(t, handlePush, first, Args{})

	second := wired(t, remote)
	blocks(t, handleBootstrap, second, Args{})
	blocks(t, handleWritePending, first, Args{"slug": "shop", "content": "## Next\n\n- [ ] one, from here\n"})
	blocks(t, handleCommit, first, Args{"message": "save(shop): here"})
	blocks(t, handlePush, first, Args{})
	blocks(t, handleWritePending, second, Args{"slug": "shop", "content": "## Next\n\n- [ ] one, from there\n"})
	blocks(t, handleCommit, second, Args{"message": "save(shop): there"})
	blocks(t, handleSync, second, Args{})

	if !second.repo().Status().Rebasing {
		t.Fatal("the store is not mid-rebase")
	}
	return second
}

// Git lets you commit mid-rebase and says nothing, the commit is folded into the
// rebase, and abort then destroys it without stashing it. So a session that did
// the two things it was told to do, in the wrong order, could lose all of its
// memory. Nothing may write while the store is half-merged.
func TestNothingWritesWhileTheStoreIsHalfMerged(t *testing.T) {
	call := conflicted(t)

	for name, args := range map[string]Args{
		"mnemo_write_memory":   {"id": "mid-merge", "projects": []any{"shop"}, "type": "decision", "body": "A fact."},
		"mnemo_write_pending":  {"slug": "shop", "content": "## Next\n\n- [ ] written onto half a merge\n"},
		"mnemo_upsert_project": {"slug": "shop", "name": "Renamed mid-merge"},
		"mnemo_commit":         {"message": "save(shop): while half-merged"},
	} {
		handle := map[string]Handler{
			"mnemo_write_memory": handleWriteMemory, "mnemo_write_pending": handleWritePending,
			"mnemo_upsert_project": handleUpsertProject, "mnemo_commit": handleCommit,
		}[name]
		said := refusal(t, handle, call, args)
		if !strings.Contains(said, "mid-merge") {
			t.Errorf("%s said:\n%s", name, said)
		}
		if !strings.Contains(said, "mnemo_resolve_conflict") {
			t.Errorf("%s does not say how to get out:\n%s", name, said)
		}
	}
	if _, err := os.Stat(memory.MemoryPath(call.StoreDir, "mid-merge")); !os.IsNotExist(err) {
		t.Error("a memory was written onto a half-applied merge")
	}
}

// A card built from files that carry conflict markers would show a user half a
// merge as fact, and the agent is told to print the card verbatim.
func TestNothingIsReadWhileTheStoreIsHalfMerged(t *testing.T) {
	call := conflicted(t)

	for name, handle := range map[string]Handler{
		"mnemo_load_project":    handleLoadProject,
		"mnemo_search_memories": handleSearch,
		"mnemo_read_memory":     handleReadMemory,
	} {
		said := refusal(t, handle, call, Args{"slug": "shop", "id": "anything"})
		if !strings.Contains(said, "conflict markers") {
			t.Errorf("%s said:\n%s", name, said)
		}
	}

	// Status is how a fresh session finds out, so it answers — and names the way
	// out rather than only reporting the state.
	got := blocks(t, handleStatus, call, Args{})
	for _, want := range []string{"a rebase is in progress", "mnemo_sync", "mnemo_resolve_conflict", "mnemo_rebase"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("status does not mention %q:\n%s", want, got[0])
		}
	}
}

// Aborting is described as the safe way out, so what it costs has to come back
// with it: the hub's memory is still unmerged and the same conflict returns.
func TestAbortingSaysWhatItCost(t *testing.T) {
	call := conflicted(t)

	got := blocks(t, handleRebase, call, Args{"action": "abort"})
	said := strings.Join(got, "\n")
	if !strings.Contains(said, "not merged") {
		t.Errorf("aborting does not say the hub's memory is still unmerged:\n%s", said)
	}
	if !strings.Contains(said, "same conflict") {
		t.Errorf("aborting does not say it will happen again:\n%s", said)
	}
	// And the store really is usable again.
	if call.repo().Status().Rebasing {
		t.Error("the store is still mid-rebase")
	}
	blocks(t, handleWriteMemory, call, Args{
		"id": "after-abort", "projects": []any{"shop"}, "type": "decision", "body": "Writing works again."})
}

// A merged file can legitimately carry a secret that came in from the hub, where
// it is already saved. Telling the agent to drop the line would have it delete a
// memory to make the call succeed.
func TestAMergeWithASecretDoesNotTellTheAgentToDeleteIt(t *testing.T) {
	call := conflicted(t)
	secret := "ghp_" + strings.Repeat("C", 36)

	said := refusal(t, handleResolveConflict, call, Args{
		"file": "projects/shop/pending.md", "content": "## Next\n\n- [ ] rotate " + secret + "\n",
	})
	if strings.Contains(said, secret) {
		t.Fatal("the refusal repeated the secret")
	}
	if !strings.Contains(said, "Do NOT remove the line") {
		t.Errorf("the refusal invites deleting the content:\n%s", said)
	}
	if strings.Contains(said, "save a note that says where it lives") {
		t.Errorf("the write-time wording leaked into the merge path:\n%s", said)
	}
	if !strings.Contains(said, "abort") {
		t.Errorf("the refusal does not offer a way out of the rebase:\n%s", said)
	}
}

// Resolving is only for the files a sync left conflicted. Without that check the
// tool replaces any file in the store wholesale, and stages it: a committed
// memory gone, with no overwrite flag and no validation.
func TestResolvingIsOnlyForConflictedFiles(t *testing.T) {
	call := writable(t, true)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})
	blocks(t, handleWriteMemory, call, Args{
		"id": "keep-me", "projects": []any{"shop"}, "type": "decision", "body": "Worth keeping."})
	blocks(t, handleCommit, call, Args{"message": "save(shop): worth keeping"})
	before := read(t, memory.MemoryPath(call.StoreDir, "keep-me"))

	said := refusal(t, handleResolveConflict, call, Args{
		"file": "memories/keep-me.md", "content": "nothing of the original left\n"})
	if !strings.Contains(said, "not one of the files a sync left conflicted") {
		t.Errorf("the refusal reads:\n%s", said)
	}
	if !strings.Contains(said, "mnemo_write_memory") {
		t.Errorf("the refusal does not point at the right tool:\n%s", said)
	}
	if after := read(t, memory.MemoryPath(call.StoreDir, "keep-me")); after != before {
		t.Error("a committed memory was replaced through the merge tool")
	}
}

// A value that is already in the conflicted file came from a commit on one side
// or the other, so it is already in the user's memory. Refusing the merge for it
// would leave this machine unable to finish the rebase and unable to ever pull
// again, with no way out but abort.
func TestAMergeIsNotRefusedForASecretThatIsAlreadyInTheFile(t *testing.T) {
	call := conflicted(t)
	onDisk := read(t, memory.PendingPath(call.StoreDir, "shop"))
	secret := "token = \"" + strings.Repeat("d", 32) + "\""
	if !strings.Contains(onDisk, "<<<<<<<") {
		t.Fatalf("the fixture is not conflicted:\n%s", onDisk)
	}

	// Put the flagged value on one side, as a hand edit on the other machine
	// would have done, then merge keeping both sides.
	withSecret := strings.Replace(onDisk, "- [ ] one, from there", "- [ ] one, from there — "+secret, 1)
	put(t, memory.PendingPath(call.StoreDir, "shop"), withSecret)

	merged := "## Next\n\n- [ ] one, from here\n- [ ] one, from there — " + secret + "\n"
	got := blocks(t, handleResolveConflict, call, Args{
		"file": "projects/shop/pending.md", "content": merged})
	if !strings.Contains(got[0], "Nothing left conflicted") {
		t.Fatalf("the merge was refused for something already in the file: %q", got)
	}

	// A value the agent introduces is still refused: the point is what the merge
	// adds, not what it contains.
	call2 := conflicted(t)
	fresh := "ghp_" + strings.Repeat("E", 36)
	said := refusal(t, handleResolveConflict, call2, Args{
		"file": "projects/shop/pending.md", "content": "## Next\n\n- [ ] rotate " + fresh + "\n"})
	if !strings.Contains(said, "Do NOT remove the line") {
		t.Errorf("a newly added secret was not refused properly:\n%s", said)
	}
}

// A hub that cannot be reached must not read as success. An agent that believes
// the work is published tells the user so, and they go looking for it.
func TestReachingAHubThatIsGone(t *testing.T) {
	remote := hub(t)
	call := wired(t, remote)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})
	blocks(t, handleCommit, call, Args{"message": "save(shop): one"})
	blocks(t, handlePush, call, Args{})

	blocks(t, handleWriteMemory, call, Args{
		"id": "after", "projects": []any{"shop"}, "type": "decision", "body": "Later."})
	blocks(t, handleCommit, call, Args{"message": "save(shop): later"})
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}

	said := refusal(t, handlePush, call, Args{})
	if !strings.Contains(said, "Nothing was published") {
		t.Errorf("a failed push reads:\n%s", said)
	}
	saidSync := refusal(t, handleSync, call, Args{})
	if !strings.Contains(saidSync, "Nothing was pulled") {
		t.Errorf("a failed sync reads:\n%s", saidSync)
	}
}
