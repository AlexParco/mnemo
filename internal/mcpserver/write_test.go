package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexParco/mnemo/internal/gitx"
	"github.com/AlexParco/mnemo/internal/lock"
	"github.com/AlexParco/mnemo/internal/memory"
	"github.com/AlexParco/mnemo/internal/store"
)

// writable is a call with a real store behind it, on a machine that looks like
// nobody's: without this the developer's own git identity leaks in and the test
// that proves mnemo reports a missing one would pass for the wrong reason.
func writable(t *testing.T, identity bool) *Call {
	t.Helper()
	home := t.TempDir()
	config := filepath.Join(home, "gitconfig")
	contents := ""
	if identity {
		contents = "[user]\n\tname = Test\n\temail = test@example.invalid\n"
	}
	if err := os.WriteFile(config, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		os.Unsetenv(name)
	}

	dir := filepath.Join(home, "store")
	return &Call{
		StoreDir: dir,
		Machine:  "here",
		Lang:     memory.EN,
		store:    store.New(dir, store.Options{Locker: lock.New(filepath.Join(home, "locks"))}),
	}
}

// refusal runs a handler and requires it to refuse, returning what it said.
//
// A refusal and an unexpected failure both set IsError, so checking the flag
// alone is not checking anything: the whole classification could be deleted and
// every test here would still pass. The two are told apart by what they say,
// because that is also how an agent tells them apart — one is advice it can act
// on, the other says there is nothing to be done.
func refusal(t *testing.T, handle Handler, call *Call, args Args) string {
	t.Helper()
	result := handle(t.Context(), call, args).result()
	said := strings.Join(textOf(t, result), "\n")
	if !result.IsError {
		t.Fatalf("expected a refusal, got %q", said)
	}
	if strings.Contains(said, "mnemo failed unexpectedly") {
		t.Fatalf("this is advice the agent could act on, reported as a crash: %s", said)
	}
	if len(textOf(t, result)) != 1 {
		t.Errorf("a refusal carries %d blocks, want exactly one", len(textOf(t, result)))
	}
	return said
}

func TestBootstrapSaysWhatItDid(t *testing.T) {
	call := writable(t, true)

	first := blocks(t, handleBootstrap, call, Args{})
	if !strings.Contains(first[0], "Created the store at "+call.StoreDir) {
		t.Errorf("the report reads:\n%s", first[0])
	}
	if !strings.Contains(first[0], "Set a hub remote with mnemo config set store.remote") {
		t.Errorf("a store with no hub is not told how to get one:\n%s", first[0])
	}
	if !strings.Contains(first[0], "Wrote: ") {
		t.Errorf("the templates are not reported:\n%s", first[0])
	}

	// Safe to call repeatedly is a promise the write tools depend on: they call
	// it themselves before every write.
	again := blocks(t, handleBootstrap, call, Args{})
	if len(again) != 1 || !strings.Contains(again[0], "was already set up; nothing to do") {
		t.Errorf("calling it twice said:\n%s", strings.Join(again, "\n"))
	}
}

// A machine with no git identity is told now, while someone is watching, and
// not at the first commit weeks later.
func TestBootstrapWarnsAboutAMissingIdentity(t *testing.T) {
	call := writable(t, false)

	got := blocks(t, handleBootstrap, call, Args{})
	if !strings.Contains(got[0], "git has no committer identity here") {
		t.Errorf("no warning:\n%s", got[0])
	}
}

func TestUpsertProject(t *testing.T) {
	call := writable(t, true)

	created := blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})
	if !strings.Contains(created[0], "Created project 'shop'") {
		t.Errorf("the first block reads %q", created[0])
	}
	if !strings.Contains(created[0], "pending.md is empty; sections are free-form") {
		t.Errorf("creating does not explain the empty pending list: %q", created[0])
	}
	// Written out, not compared to the constant: comparing a value to itself
	// would let the sentence be rewritten to say the opposite.
	if created[1] != "Not committed yet — this is normal. Keep writing, and call mnemo_commit ONCE "+
		"when the session's writes are all done. Do not commit after each write." {
		t.Errorf("the second block is %q", created[1])
	}

	updated := blocks(t, handleUpsertProject, call, Args{"slug": "shop", "status": "paused"})
	if !strings.Contains(updated[0], "Updated project 'shop'") {
		t.Errorf("the second call said %q", updated[0])
	}
	project, ok := memory.ReadProject(call.StoreDir, "shop")
	if !ok || project.Status != "paused" || project.Name != "Shop" {
		t.Errorf("the project is %+v; an update must touch only what was given", project)
	}
}

func TestWriteMemory(t *testing.T) {
	call := writable(t, true)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})

	got := blocks(t, handleWriteMemory, call, Args{
		"id": "plain-text-wins", "projects": []any{"shop"}, "type": "decision",
		"body": "We chose plain text over a database.",
	})
	if !strings.Contains(got[0], "Wrote memory 'plain-text-wins'") || !strings.Contains(got[0], notCommitted) {
		t.Errorf("the first block reads %q", got[0])
	}
	// The same sentence after every write, and it has to say "not after each
	// one": a per-write nudge read ten times beats a description read once.
	if !strings.Contains(got[0], "Do not commit after each write") {
		t.Errorf("the write does not discourage committing each time: %q", got[0])
	}

	t.Run("an existing id needs overwrite", func(t *testing.T) {
		said := refusal(t, handleWriteMemory, call, Args{
			"id": "plain-text-wins", "projects": []any{"shop"}, "type": "decision", "body": "Something else.",
		})
		if !strings.Contains(said, "already exists") || !strings.Contains(said, "overwrite: true") {
			t.Errorf("the refusal reads:\n%s", said)
		}
		// The refusal has to leave the old body alone.
		if body := read(t, memory.MemoryPath(call.StoreDir, "plain-text-wins")); !strings.Contains(body, "over a database") {
			t.Error("the refused write changed the memory anyway")
		}
	})

	t.Run("a project that does not exist", func(t *testing.T) {
		said := refusal(t, handleWriteMemory, call, Args{
			"id": "orphan", "projects": []any{"nowhere"}, "type": "decision", "body": "A fact.",
		})
		if !strings.Contains(said, "nowhere") {
			t.Errorf("the refusal does not name the slug:\n%s", said)
		}
		if _, err := os.Stat(memory.MemoryPath(call.StoreDir, "orphan")); !os.IsNotExist(err) {
			t.Error("a memory was written for a project that does not exist")
		}
	})
}

// A secret is refused before it reaches the disk, and the refusal never repeats
// the value: an excerpt that echoed it would put it in the transcript.
func TestASecretIsRefusedAndNothingIsWritten(t *testing.T) {
	call := writable(t, true)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})
	secret := "ghp_" + strings.Repeat("A", 36)

	said := refusal(t, handleWriteMemory, call, Args{
		"id": "the-token", "projects": []any{"shop"}, "type": "reference",
		"body": "The deploy token is " + secret,
	})
	if !strings.Contains(said, "That looks like a secret") {
		t.Errorf("the refusal reads:\n%s", said)
	}
	if strings.Contains(said, secret) {
		t.Error("the refusal repeated the secret back")
	}
	if _, err := os.Stat(memory.MemoryPath(call.StoreDir, "the-token")); !os.IsNotExist(err) {
		t.Error("the memory was written anyway")
	}

	// A pending list goes through the same scan.
	saidAgain := refusal(t, handleWritePending, call, Args{
		"slug": "shop", "content": "## Next\n\n- [ ] rotate " + secret + "\n",
	})
	if !strings.Contains(saidAgain, "That looks like a secret") || strings.Contains(saidAgain, secret) {
		t.Errorf("the pending refusal reads:\n%s", saidAgain)
	}
}

func TestWritePendingReportsWhatItParsed(t *testing.T) {
	call := writable(t, true)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})

	got := blocks(t, handleWritePending, call, Args{"slug": "shop", "content": "" +
		"## In progress\n\n- [ ] wire the parser\n\n" +
		"## Next\n\n- [ ] the card\n- [ ] the server\n\n" +
		"## Done\n\n- [x] chose the format\n"})

	if !strings.HasPrefix(got[0], "Rewrote pending.md for 'shop'. ") || !strings.Contains(got[0], notCommitted) {
		t.Errorf("the first block reads %q", got[0])
	}
	// The file was replaced wholesale, so an agent that meant to write three
	// sections needs to see three come back.
	for _, want := range []string{
		"\"label\": \"In progress\"", "\"open\": 1",
		"\"label\": \"Next\"", "\"open\": 2",
		"\"label\": \"Done\"", "\"done\": 1",
	} {
		if !strings.Contains(got[1], want) {
			t.Errorf("the summary does not contain %s:\n%s", want, got[1])
		}
	}
}

func TestCommit(t *testing.T) {
	call := writable(t, true)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})

	got := blocks(t, handleCommit, call, Args{"message": "save(shop): the first one"})
	if !strings.Contains(got[0], "Committed ") || !strings.Contains(got[0], "file(s):") {
		t.Errorf("the block reads:\n%s", got[0])
	}
	// A store with no hub says so rather than counting commits nobody can see.
	if !strings.Contains(got[0], "This store has no remote, so it lives only on this machine.") {
		t.Errorf("the block does not say where the work is:\n%s", got[0])
	}

	again := blocks(t, handleCommit, call, Args{"message": "save(shop): nothing changed"})
	if again[0] != "Nothing to commit: the store has no pending changes." {
		t.Errorf("a second commit said %q", again[0])
	}
}

// The store does not carry co-authorship, and the agent is told when a trailer
// was taken out rather than having it happen silently.
func TestCommitStripsCoAuthorship(t *testing.T) {
	call := writable(t, true)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})

	got := blocks(t, handleCommit, call, Args{
		"message": "save(shop): a change\n\nCo-Authored-By: Someone <someone@example.invalid>\n",
	})
	if !strings.Contains(got[0], "Removed 1 Co-Authored-By trailer(s)") {
		t.Errorf("the removal was not reported:\n%s", got[0])
	}
	log, ok := call.repo().Try("log", "-1", "--format=%B")
	if !ok {
		t.Fatal("no commit to read back")
	}
	if strings.Contains(log, "Co-Authored-By") {
		t.Errorf("the trailer reached the history:\n%s", log)
	}
}

// A connection that may only read says so, rather than crashing on a store that
// is not there.
func TestAReadOnlyConnectionRefusesToWrite(t *testing.T) {
	call := &Call{StoreDir: t.TempDir(), Machine: "here", Lang: memory.EN}

	for name, handle := range map[string]Handler{
		"mnemo_bootstrap":      handleBootstrap,
		"mnemo_upsert_project": handleUpsertProject,
		"mnemo_write_memory":   handleWriteMemory,
		"mnemo_write_pending":  handleWritePending,
		"mnemo_commit":         handleCommit,
	} {
		said := refusal(t, handle, call, Args{"slug": "shop", "id": "x", "message": "m"})
		if !strings.Contains(said, "can read the memory but not change it") {
			t.Errorf("%s said %q", name, said)
		}
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

// A fact that changed is superseded, not overwritten: both memories stay, so
// someone asking why the answer changed can see it. The refusal an agent
// actually hits has to name that route, or it will reach for the one it was
// told about and the reason will be gone.
func TestTheOverwriteRefusalOffersBothWaysOut(t *testing.T) {
	call := writable(t, true)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})
	blocks(t, handleWriteMemory, call, Args{
		"id": "we-use-postgres", "projects": []any{"shop"}, "type": "decision", "body": "Postgres it is.",
	})

	said := refusal(t, handleWriteMemory, call, Args{
		"id": "we-use-postgres", "projects": []any{"shop"}, "type": "decision", "body": "Actually sqlite.",
	})
	for _, want := range []string{"overwrite: true", "supersedes: 'we-use-postgres'", "the fact has CHANGED"} {
		if !strings.Contains(said, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, said)
		}
	}

	// And superseding really does keep both, with the agent told so.
	got := blocks(t, handleWriteMemory, call, Args{
		"id": "we-use-sqlite", "projects": []any{"shop"}, "type": "decision",
		"body": "Sqlite now.", "supersedes": "we-use-postgres",
	})
	if len(got) < 2 || !strings.Contains(got[1], "is kept and marked as superseded") {
		t.Errorf("the answer does not say the old one survived: %q", got)
	}
	if _, err := os.Stat(memory.MemoryPath(call.StoreDir, "we-use-postgres")); err != nil {
		t.Error("the superseded memory was deleted")
	}
}

// An agent that cannot write is told so; an agent whose store has no path is
// told something different, because the two need different things done.
func TestAStoreWithNoPathIsRefusedByName(t *testing.T) {
	call := writable(t, true)
	call.store = store.New("", store.Options{})

	said := refusal(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})
	if !strings.Contains(said, "does not know where the store should be") {
		t.Errorf("the refusal reads:\n%s", said)
	}
	if !strings.Contains(said, "MNEMO_DIR") {
		t.Errorf("the refusal does not say what to set:\n%s", said)
	}
}

// A refusal is one block by contract. Chaining onto an answer that has already
// failed used to append blocks describing a success that did not happen.
func TestAFailedAnswerAcceptsNothingMore(t *testing.T) {
	a := (&answer{}).refuse("no").say("and another thing").block("and another").data([]string{"x"})
	result := a.result()
	if !result.IsError {
		t.Fatal("the answer is not a refusal")
	}
	if got := textOf(t, result); len(got) != 1 || got[0] != "no" {
		t.Errorf("a refusal carries %d blocks: %q", len(got), got)
	}

	failed := (&answer{}).fail("broke").say("more").result()
	if got := textOf(t, failed); len(got) != 1 || got[0] != "mnemo failed unexpectedly: broke" {
		t.Errorf("a failure carries %q", got)
	}
}

// hub is an empty bare repository, standing in for the machine the user syncs
// through.
func hub(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "hub.git")
	if _, err := gitx.New(filepath.Dir(dir)).Run("init", "-q", "--bare", dir); err != nil {
		t.Fatalf("creating the hub: %v", err)
	}
	return dir
}

// Everything the bootstrap report can say, which is the half of it that only
// happens on a machine with a hub — the machine where getting it wrong means a
// person cannot tell whether their memory arrived.
func TestBootstrapReportsTheHub(t *testing.T) {
	t.Run("wired, with nothing on it yet", func(t *testing.T) {
		call := writable(t, true)
		remote := hub(t)
		call.store = store.New(call.StoreDir, store.Options{
			Locker: lock.New(filepath.Join(filepath.Dir(call.StoreDir), "locks")), Remote: remote,
		})

		got := blocks(t, handleBootstrap, call, Args{})
		if !strings.Contains(got[0], "Wired remote: "+remote) {
			t.Errorf("the hub is not named:\n%s", got[0])
		}
		// Advising a hub to a store that has one would contradict the line
		// right above it.
		if strings.Contains(got[0], "Set a hub remote") {
			t.Errorf("it advised setting a hub it already has:\n%s", got[0])
		}
	})

	t.Run("adopting what is already on it", func(t *testing.T) {
		remote := hub(t)
		first := writable(t, true)
		first.store = store.New(first.StoreDir, store.Options{
			Locker: lock.New(filepath.Join(filepath.Dir(first.StoreDir), "locks")), Remote: remote,
		})
		blocks(t, handleUpsertProject, first, Args{"slug": "shop", "name": "Shop"})
		blocks(t, handleCommit, first, Args{"message": "save(shop): the first machine"})
		if pushed := first.repo().Push(""); !pushed.Pushed {
			t.Fatalf("publishing: %+v", pushed)
		}

		second := writable(t, true)
		second.store = store.New(second.StoreDir, store.Options{
			Locker: lock.New(filepath.Join(filepath.Dir(second.StoreDir), "locks")), Remote: remote,
		})
		got := blocks(t, handleBootstrap, second, Args{})
		if !strings.Contains(got[0], "Adopted the existing memory from the hub") {
			t.Errorf("the second machine was not told it adopted:\n%s", got[0])
		}
		if !memory.ProjectExists(second.StoreDir, "shop") {
			t.Error("the other machine's project did not arrive")
		}
	})
}

// A write tool must carry every argument it was given through to the store. A
// dropped one is silent: the write succeeds and the fact is wrong.
func TestWriteMemoryCarriesEveryField(t *testing.T) {
	call := writable(t, true)
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})

	blocks(t, handleWriteMemory, call, Args{
		"id": "first-answer", "projects": []any{"shop"}, "type": "decision",
		"body": "The first answer.", "services": []any{"api", "web"},
		"tags": []any{"storage"}, "author": "Someone Else",
	})
	written, ok := findMemory(t, call.StoreDir, "first-answer")
	if !ok {
		t.Fatal("nothing was written")
	}
	if strings.Join(written.Services, ",") != "api,web" || strings.Join(written.Tags, ",") != "storage" {
		t.Errorf("services and tags did not arrive: %+v", written)
	}
	if written.Author != "Someone Else" {
		t.Errorf("author is %q, want the one that was given", written.Author)
	}

	t.Run("overwrite really replaces, and says it updated", func(t *testing.T) {
		got := blocks(t, handleWriteMemory, call, Args{
			"id": "first-answer", "projects": []any{"shop"}, "type": "decision",
			"body": "A corrected answer.", "overwrite": true,
		})
		if !strings.HasPrefix(got[0], "Updated memory 'first-answer'") {
			t.Errorf("a replacement reported itself as %q", got[0])
		}
		if !strings.Contains(got[0], memory.MemoryPath(call.StoreDir, "first-answer")) {
			t.Errorf("the answer does not say where it went: %q", got[0])
		}
		again, _ := findMemory(t, call.StoreDir, "first-answer")
		if !strings.Contains(again.Body, "A corrected answer.") {
			t.Errorf("the body was not replaced: %q", again.Body)
		}
	})

	t.Run("a near-duplicate is named", func(t *testing.T) {
		got := blocks(t, handleWriteMemory, call, Args{
			"id": "first-answer-again", "projects": []any{"shop"}, "type": "decision",
			"body": "A corrected answer, said twice.",
		})
		joined := strings.Join(got, "\n")
		if !strings.Contains(joined, "share vocabulary") || !strings.Contains(joined, "first-answer") {
			t.Errorf("the store's only defence against one fact in two files said nothing:\n%s", joined)
		}
	})
}

// Committing on a machine with a hub has to say the work has not left it.
func TestCommitSaysWhatTheHubHasNotSeen(t *testing.T) {
	call := writable(t, true)
	remote := hub(t)
	call.store = store.New(call.StoreDir, store.Options{
		Locker: lock.New(filepath.Join(filepath.Dir(call.StoreDir), "locks")), Remote: remote,
	})
	blocks(t, handleUpsertProject, call, Args{"slug": "shop", "name": "Shop"})

	got := blocks(t, handleCommit, call, Args{"message": "save(shop): not pushed"})
	if !strings.Contains(got[0], "not on the hub yet") {
		t.Errorf("the commit does not say the work is unpublished:\n%s", got[0])
	}
	if strings.Contains(got[0], "This store has no remote") {
		t.Errorf("a store with a hub was reported as having none:\n%s", got[0])
	}
	// The files are named, so a person can see what the session actually wrote.
	if !strings.Contains(got[0], "projects/shop/INDEX.md") {
		t.Errorf("the commit does not list the files:\n%s", got[0])
	}
}

func findMemory(t *testing.T, dir, id string) (memory.Memory, bool) {
	t.Helper()
	for _, m := range memory.LoadMemories(dir) {
		if m.ID == id {
			return m, true
		}
	}
	return memory.Memory{}, false
}
