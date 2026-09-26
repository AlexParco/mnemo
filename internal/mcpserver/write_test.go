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
func refusal(t *testing.T, handle Handler, call *Call, args Args) string {
	t.Helper()
	result := handle(t.Context(), call, args).result()
	if !result.IsError {
		t.Fatalf("expected a refusal, got %q", textOf(t, result))
	}
	return strings.Join(textOf(t, result), "\n")
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
	if created[1] != notCommitted {
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
