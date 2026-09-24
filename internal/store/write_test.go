package store

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/AlexParco/mnemo/internal/memory"
)

// ready is a store with one project in it, which is what most writes need.
func ready(t *testing.T) *Store {
	t.Helper()
	isolate(t, true)
	s := newStore(t, Options{})
	if _, err := s.UpsertProject(t.Context(), ProjectInput{Slug: "a-project", Name: "A Project"}); err != nil {
		t.Fatalf("creating a project: %v", err)
	}
	return s
}

func TestWriteMemoryHasTheShapeTheContractGives(t *testing.T) {
	s := ready(t)

	got, err := s.WriteMemory(t.Context(), MemoryInput{
		ID:       "token-rotation",
		Projects: []string{"a-project"},
		Services: []string{"api", "workers"},
		Tags:     []string{"auth"},
		Type:     "decision",
		Body:     "  Access tokens rotate every 15 minutes.  ",
	})
	if err != nil {
		t.Fatalf("writing: %v", err)
	}
	if !got.Created {
		t.Error("a new memory was not reported as created")
	}

	want := "---\n" +
		"id: token-rotation\n" +
		"projects: [a-project]\n" +
		"services: [api, workers]\n" +
		"tags: [auth]\n" +
		"type: decision\n" +
		"author: Test\n" +
		"updated: 2026-03-04\n" +
		"---\n\n" +
		"Access tokens rotate every 15 minutes.\n"
	if on := read(t, got.Path); on != want {
		t.Errorf("the file is:\n%q\nwant:\n%q", on, want)
	}
}

func TestWriteMemoryLeavesOutWhatWasNotGiven(t *testing.T) {
	s := ready(t)
	got, err := s.WriteMemory(t.Context(), MemoryInput{
		ID: "bare", Projects: []string{"a-project"}, Type: "todo", Body: "Do the thing.",
	})
	if err != nil {
		t.Fatal(err)
	}
	on := read(t, got.Path)
	if strings.Contains(on, "services:") || strings.Contains(on, "tags:") {
		t.Errorf("optional fields were written anyway:\n%s", on)
	}
}

func TestWriteMemoryRefusesWhatTheContractForbids(t *testing.T) {
	s := ready(t)
	cases := []struct {
		name string
		in   MemoryInput
		says string
	}{
		{"an id that is not kebab-case", MemoryInput{ID: "Not Kebab", Projects: []string{"a-project"}, Type: "decision", Body: "x"}, "kebab-case"},
		{"a type outside the six", MemoryInput{ID: "ok", Projects: []string{"a-project"}, Type: "opinion", Body: "x"}, "not a memory type"},
		{"a body that is only spaces", MemoryInput{ID: "ok", Projects: []string{"a-project"}, Type: "decision", Body: "   \n"}, "needs a body"},
		{"no project at all", MemoryInput{ID: "ok", Projects: nil, Type: "decision", Body: "x"}, "at least one project"},
		{"a project that does not exist", MemoryInput{ID: "ok", Projects: []string{"ghost"}, Type: "decision", Body: "x"}, "never silently"},
		{"an id that climbs out of the store", MemoryInput{ID: "../escape", Projects: []string{"a-project"}, Type: "decision", Body: "x"}, "kebab-case"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.WriteMemory(t.Context(), c.in)
			if !IsRefusal(err) {
				t.Fatalf("got %v, want a refusal", err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the message is %q, want it to mention %q", err, c.says)
			}
		})
	}
}

func TestOverwritingMustBeAskedForAndKeepsWhatItDoesNotTouch(t *testing.T) {
	s := ready(t)
	path := memory.MemoryPath(s.Dir, "note")

	// A memory written by hand, with a key mnemo knows nothing about.
	original := "---\nid: note\nprojects: [a-project]\ntype: gotcha\nauthor: Someone Else\n" +
		"updated: 2026-01-01\nreviewed-by: a-colleague\n---\n\nThe first version.\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	in := MemoryInput{ID: "note", Projects: []string{"a-project"}, Type: "gotcha", Body: "The second version."}
	if _, err := s.WriteMemory(t.Context(), in); !IsRefusal(err) {
		t.Fatalf("overwriting without saying so gave %v, want a refusal", err)
	}
	if read(t, path) != original {
		t.Fatal("the refused write changed the file")
	}

	in.Overwrite = true
	if _, err := s.WriteMemory(t.Context(), in); err != nil {
		t.Fatalf("overwriting: %v", err)
	}
	after := read(t, path)
	if !strings.Contains(after, "reviewed-by: a-colleague") {
		t.Errorf("a key mnemo does not understand was dropped:\n%s", after)
	}
	if !strings.Contains(after, "author: Someone Else") {
		t.Errorf("the original author was replaced:\n%s", after)
	}
	if !strings.Contains(after, "updated: 2026-03-04") || !strings.Contains(after, "The second version.") {
		t.Errorf("the update did not land:\n%s", after)
	}
}

func TestNearDuplicatesAreSurfacedNotBlocked(t *testing.T) {
	s := ready(t)
	for _, id := range []string{"token-rotation-policy", "unrelated-thing"} {
		if _, err := s.WriteMemory(t.Context(), MemoryInput{
			ID: id, Projects: []string{"a-project"}, Type: "decision", Body: "Something about " + id + ".",
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.WriteMemory(t.Context(), MemoryInput{
		ID: "token-rotation-window", Projects: []string{"a-project"}, Type: "decision", Body: "A second note on the same thing.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Related, ",") != "token-rotation-policy" {
		t.Errorf("related = %q, want the memory that shares its words", got.Related)
	}
}

// The push scan is the last chance to stop a secret. This is the first, and it
// has to leave nothing behind: not the file, not a temporary one.
func TestASecretNeverReachesTheDisk(t *testing.T) {
	s := ready(t)
	secret := "AKIA" + strings.Repeat("Q", 16)

	_, err := s.WriteMemory(t.Context(), MemoryInput{
		ID: "leak", Projects: []string{"a-project"}, Type: "reference", Body: "aws_key = " + secret,
	})
	if !IsRefusal(err) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Error("the refusal repeats the secret it is refusing")
	}
	if !strings.Contains(err.Error(), "AWS access key id") {
		t.Errorf("the refusal does not say what it found: %s", err)
	}

	if _, err := os.Stat(memory.MemoryPath(s.Dir, "leak")); !os.IsNotExist(err) {
		t.Error("the memory was written anyway")
	}
	entries, err := os.ReadDir(memory.MemoriesDir(s.Dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), tempPrefix) {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestASecretInAPendingListIsRefusedAndTheOldListStays(t *testing.T) {
	s := ready(t)
	path := memory.PendingPath(s.Dir, "a-project")
	before := read(t, path)

	_, err := s.WritePending(t.Context(), "a-project", "# Pending\n\n## Next\n- [ ] use token: "+strings.Repeat("k", 16)+"\n")
	if !IsRefusal(err) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if read(t, path) != before {
		t.Error("the refused write changed the pending list")
	}
}

func TestSupersedingKeepsBothAndPointsForward(t *testing.T) {
	s := ready(t)
	write := func(id, body, supersedes string) MemoryResult {
		t.Helper()
		got, err := s.WriteMemory(t.Context(), MemoryInput{
			ID: id, Projects: []string{"a-project"}, Type: "decision", Body: body, Supersedes: supersedes,
		})
		if err != nil {
			t.Fatalf("writing %s: %v", id, err)
		}
		return got
	}

	write("rotate-hourly", "Tokens rotate every hour.", "")
	second := write("rotate-quarter-hourly", "Tokens rotate every fifteen minutes.", "rotate-hourly")
	if second.Superseded != "rotate-hourly" {
		t.Errorf("the result does not say what it replaced: %+v", second)
	}

	old := memory.ParseDoc(read(t, memory.MemoryPath(s.Dir, "rotate-hourly")))
	if got := old.Fields.Str("superseded_by"); got != "rotate-quarter-hourly" {
		t.Errorf("the old memory points at %q, want the new one", got)
	}
	if got := old.Fields.Str("updated"); got != "2026-03-04" {
		t.Errorf("the old memory's date is %q, want the day it was superseded", got)
	}
	if !strings.Contains(old.Body, "every hour") {
		t.Error("the old memory lost its body; superseding is not deleting")
	}

	// Both are still found: asking why something changed needs both answers.
	if got := memory.Search(s.Dir, memory.SearchOptions{Query: "rotate"}); len(got) != 2 {
		t.Errorf("search found %d memories, want both versions", len(got))
	}

	// A chain is allowed.
	write("rotate-on-use", "Tokens rotate on use.", "rotate-quarter-hourly")
	middle := memory.ParseDoc(read(t, memory.MemoryPath(s.Dir, "rotate-quarter-hourly")))
	if got := middle.Fields.Str("superseded_by"); got != "rotate-on-use" {
		t.Errorf("the middle of the chain points at %q", got)
	}
}

func TestSupersedingRefusals(t *testing.T) {
	s := ready(t)
	if _, err := s.WriteMemory(t.Context(), MemoryInput{
		ID: "first", Projects: []string{"a-project"}, Type: "decision", Body: "One.",
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("a memory that does not exist", func(t *testing.T) {
		_, err := s.WriteMemory(t.Context(), MemoryInput{
			ID: "second", Projects: []string{"a-project"}, Type: "decision", Body: "Two.", Supersedes: "ghost",
		})
		if !IsRefusal(err) {
			t.Fatalf("got %v, want a refusal", err)
		}
		if _, err := os.Stat(memory.MemoryPath(s.Dir, "second")); !os.IsNotExist(err) {
			t.Error("the new memory was written even though the refusal stood")
		}
	})

	t.Run("itself", func(t *testing.T) {
		_, err := s.WriteMemory(t.Context(), MemoryInput{
			ID: "first", Projects: []string{"a-project"}, Type: "decision", Body: "One.", Overwrite: true, Supersedes: "first",
		})
		if !IsRefusal(err) {
			t.Fatalf("got %v, want a refusal", err)
		}
	})

	t.Run("a cycle", func(t *testing.T) {
		if _, err := s.WriteMemory(t.Context(), MemoryInput{
			ID: "second", Projects: []string{"a-project"}, Type: "decision", Body: "Two.", Supersedes: "first",
		}); err != nil {
			t.Fatal(err)
		}
		// first is already superseded by second; second must not close the loop.
		_, err := s.WriteMemory(t.Context(), MemoryInput{
			ID: "first", Projects: []string{"a-project"}, Type: "decision", Body: "One again.", Overwrite: true, Supersedes: "second",
		})
		if !IsRefusal(err) {
			t.Fatalf("got %v, want a refusal", err)
		}
		if !strings.Contains(err.Error(), "close a loop") {
			t.Errorf("the message is %q", err)
		}
	})
}

func TestWritePendingReplacesAndReportsWhatItParsed(t *testing.T) {
	s := ready(t)
	content := "# Pending — A Project\n\n## In progress\n- [ ] one\n- [x] done already\n\n## Blocked\n- [ ] waiting\n"

	got, err := s.WritePending(t.Context(), "a-project", content)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, got.Path) != content {
		t.Error("the file is not what was sent")
	}
	if len(got.Sections) != 2 {
		t.Fatalf("sections = %+v, want two", got.Sections)
	}
	if got.Sections[0].Open != 1 || got.Sections[0].Done != 1 {
		t.Errorf("first section = %+v, want one open and one done", got.Sections[0])
	}

	if _, err := s.WritePending(t.Context(), "ghost", "# Pending\n"); !IsRefusal(err) {
		t.Errorf("writing to a project that does not exist gave %v, want a refusal", err)
	}
}

func TestUpsertProject(t *testing.T) {
	isolate(t, true)
	s := newStore(t, Options{})

	description := "What it is."
	got, err := s.UpsertProject(t.Context(), ProjectInput{
		Slug: "new-thing", Name: "New Thing", Services: []string{"api"}, Description: &description,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Created {
		t.Error("a new project was not reported as created")
	}

	want := "---\nslug: new-thing\nname: New Thing\nstatus: active\nservices: [api]\nupdated: 2026-03-04\n---\n\n# New Thing\n\nWhat it is.\n"
	if on := read(t, got.Path); on != want {
		t.Errorf("INDEX.md is:\n%q\nwant:\n%q", on, want)
	}
	if on := read(t, memory.PendingPath(s.Dir, "new-thing")); on != "# Pending — New Thing\n" {
		t.Errorf("pending.md is %q", on)
	}

	t.Run("an update touches only what it is given", func(t *testing.T) {
		if _, err := s.UpsertProject(t.Context(), ProjectInput{Slug: "new-thing", Status: "paused"}); err != nil {
			t.Fatal(err)
		}
		after := read(t, got.Path)
		if !strings.Contains(after, "status: paused") || !strings.Contains(after, "name: New Thing") {
			t.Errorf("the update changed the wrong things:\n%s", after)
		}
		if !strings.Contains(after, "What it is.") {
			t.Errorf("the description was dropped:\n%s", after)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		if _, err := s.UpsertProject(t.Context(), ProjectInput{Slug: "Not Kebab", Name: "x"}); !IsRefusal(err) {
			t.Errorf("an invalid slug gave %v", err)
		}
		if _, err := s.UpsertProject(t.Context(), ProjectInput{Slug: "nameless"}); !IsRefusal(err) {
			t.Errorf("creating without a name gave %v", err)
		}
		if _, err := s.UpsertProject(t.Context(), ProjectInput{Slug: "new-thing", Status: "sleeping"}); !IsRefusal(err) {
			t.Errorf("an invalid status gave %v", err)
		}
	})
}

func TestCommit(t *testing.T) {
	s := ready(t)

	t.Run("one session, one commit", func(t *testing.T) {
		got, err := s.Commit(t.Context(), "save(a-project): the first batch")
		if err != nil {
			t.Fatal(err)
		}
		if !got.Committed || got.SHA == "" {
			t.Fatalf("commit = %+v, want it recorded", got)
		}
		if len(got.Files) < 2 {
			t.Errorf("files = %q, want everything written so far", got.Files)
		}
		if got.HasRemote {
			t.Error("a store with no hub says it has one")
		}
	})

	t.Run("nothing new is not a failure", func(t *testing.T) {
		got, err := s.Commit(t.Context(), "save: again")
		if err != nil {
			t.Fatalf("committing a clean store: %v", err)
		}
		if got.Committed {
			t.Error("it committed with nothing to commit")
		}
	})

	t.Run("the store never carries a co-author trailer", func(t *testing.T) {
		writeSomething(t, s)
		got, err := s.Commit(t.Context(), "save: work\n\nCo-Authored-By: Some Model <noreply@example.invalid>\n")
		if err != nil {
			t.Fatal(err)
		}
		if got.StrippedTrailers != 1 {
			t.Errorf("stripped %d trailers, want one", got.StrippedTrailers)
		}
		if message, _ := s.Repo().Try("log", "-1", "--format=%B"); strings.Contains(message, "Co-Authored-By") {
			t.Errorf("the trailer reached the history:\n%s", message)
		}
	})

	t.Run("an empty message is refused", func(t *testing.T) {
		if _, err := s.Commit(t.Context(), "  \n"); !IsRefusal(err) {
			t.Errorf("got %v, want a refusal", err)
		}
	})
}

func TestCommitWithoutAnIdentitySaysHowToFixIt(t *testing.T) {
	isolate(t, false)
	s := newStore(t, Options{})
	if _, err := s.UpsertProject(t.Context(), ProjectInput{Slug: "a-project", Name: "A Project"}); err != nil {
		t.Fatal(err)
	}

	_, err := s.Commit(t.Context(), "save")
	if !IsRefusal(err) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "user.email") {
		t.Errorf("the message does not say how to fix it: %s", err)
	}
}

// A reader holds no lock, so it must never catch a file half-written.
func TestAReaderNeverSeesHalfAFile(t *testing.T) {
	s := ready(t)
	body := strings.Repeat("a line of a memory that is long enough to be written in pieces\n", 400)

	first := MemoryInput{ID: "big", Projects: []string{"a-project"}, Type: "reference", Body: body, Overwrite: true}
	second := first
	second.Body = strings.ToUpper(body)
	if _, err := s.WriteMemory(t.Context(), first); err != nil {
		t.Fatal(err)
	}

	path := memory.MemoryPath(s.Dir, "big")
	wantA := s.newMemory(first, "2026-03-04")
	wantB := s.newMemory(second, "2026-03-04")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			content, err := os.ReadFile(path)
			if err != nil {
				continue // the rename is atomic, but an open can still race a replace
			}
			if got := string(content); got != wantA && got != wantB {
				t.Errorf("a reader saw a file that is neither version, %d bytes long", len(got))
				return
			}
		}
	}()

	for i := 0; i < 20; i++ {
		in := first
		if i%2 == 1 {
			in = second
		}
		if _, err := s.WriteMemory(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

// Several agents write to one store at once. Every write must land.
func TestConcurrentWritesAllLand(t *testing.T) {
	s := ready(t)

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := s.WriteMemory(t.Context(), MemoryInput{
				ID:       "fact-" + string(rune('a'+n)),
				Projects: []string{"a-project"},
				Type:     "decision",
				Body:     "One of many written at the same time.",
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent write failed: %v", err)
		}
	}

	found := memory.MemoriesForProject(memory.LoadMemories(s.Dir), "a-project")
	if len(found) != writers {
		t.Errorf("%d memories landed, want %d", len(found), writers)
	}
	if _, err := s.Commit(t.Context(), "save: all of them"); err != nil {
		t.Fatal(err)
	}
	if s.Repo().Status().Dirty {
		t.Error("the tree is dirty after committing everything")
	}
}

// Writing files concurrently is safe on its own: each one lands atomically under
// its own name. Committing is not, because git has one index and one HEAD, and
// two commits at the same time collide on them. This is what the lock is for, and
// it is the failure the previous version of mnemo actually hit.
func TestConcurrentSavesDoNotCollideInGit(t *testing.T) {
	s := ready(t)

	const savers = 6
	var wg sync.WaitGroup
	errs := make(chan error, savers*2)
	for i := 0; i < savers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := s.WriteMemory(t.Context(), MemoryInput{
				ID:       "saved-" + string(rune('a'+n)),
				Projects: []string{"a-project"},
				Type:     "decision",
				Body:     "Written and committed at the same time as the others.",
			})
			errs <- err
			_, err = s.Commit(t.Context(), "save: one of several at once")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent save failed: %v", err)
		}
	}

	if s.Repo().Status().Dirty {
		t.Error("something was written but never committed")
	}
	files, _ := s.Repo().Try("ls-tree", "-r", "--name-only", "HEAD")
	for i := 0; i < savers; i++ {
		want := "memories/saved-" + string(rune('a'+i)) + ".md"
		if !strings.Contains(files, want) {
			t.Errorf("%s is not in the history:\n%s", want, files)
		}
	}
}

func TestStoreFilesAreNeverWrittenOutsideTheStore(t *testing.T) {
	s := ready(t)
	outside := filepath.Join(filepath.Dir(s.Dir), "outside.md")

	for _, slug := range []string{"../outside", "a-project/../../outside"} {
		if _, err := s.WritePending(t.Context(), slug, "# Pending\n"); !IsRefusal(err) {
			t.Errorf("a slug that climbs out gave %v, want a refusal", err)
		}
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Error("something was written outside the store")
	}
}
