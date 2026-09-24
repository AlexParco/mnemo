package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexParco/mnemo/internal/gitx"
	"github.com/AlexParco/mnemo/internal/lock"
	"github.com/AlexParco/mnemo/internal/memory"
	"github.com/AlexParco/mnemo/templates"
)

// fixedDay is what Today returns in tests, so a written file is byte-for-byte
// predictable.
var fixedDay = time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)

// isolate cuts the test off from the machine it runs on. Without it the
// developer's own git identity leaks in, and the test that proves mnemo reports
// a missing one would pass for the wrong reason.
func isolate(t *testing.T, identity bool) {
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
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		os.Unsetenv(name)
	}
}

// newStore is a store that does not exist yet, with its lock outside it.
func newStore(t *testing.T, opts Options) *Store {
	t.Helper()
	base := t.TempDir()
	if opts.Locker == nil {
		opts.Locker = lock.New(filepath.Join(base, "locks"))
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return fixedDay }
	}
	return New(filepath.Join(base, "store"), opts)
}

func newHub(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "hub.git")
	if _, err := gitx.New(filepath.Dir(dir)).Run("init", "-q", "--bare", dir); err != nil {
		t.Fatalf("creating the hub: %v", err)
	}
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(content)
}

func TestEnsureProvisionsAFreshStore(t *testing.T) {
	isolate(t, true)
	s := newStore(t, Options{})

	report, err := s.Ensure(t.Context())
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if !report.CreatedStore || !report.InitialisedRepo {
		t.Errorf("report is %+v, want a store and a repository that were created", report)
	}
	for _, sub := range []string{"projects", "memories", "shared"} {
		if info, err := os.Stat(filepath.Join(s.Dir, sub)); err != nil || !info.IsDir() {
			t.Errorf("%s is missing", sub)
		}
	}
	if !s.Repo().IsOwnRepo() {
		t.Error("the store is not a repository of its own")
	}
	if got := read(t, filepath.Join(s.Dir, ".gitignore")); got != ".DS_Store\n" {
		t.Errorf(".gitignore is %q", got)
	}
	if !report.HasIdentity || report.Identity.Name != "Test" {
		t.Errorf("identity is %+v (%v), want the configured one", report.Identity, report.HasIdentity)
	}
	want := []string{".gitignore", "shared/SCHEMA.md"}
	if strings.Join(report.WroteTemplates, ",") != strings.Join(want, ",") {
		t.Errorf("wrote %q, want %q", report.WroteTemplates, want)
	}
}

// The contract that lands in a user's store is the one in this repository, not a
// copy that drifted from it.
func TestTheSchemaWrittenIsTheContract(t *testing.T) {
	isolate(t, true)
	s := newStore(t, Options{})
	if _, err := s.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}

	onDisk := read(t, filepath.Join(s.Dir, "shared", "SCHEMA.md"))
	if onDisk != templates.Schema {
		t.Error("the store's SCHEMA.md is not the embedded one")
	}
	inRepo := read(t, filepath.Join("..", "..", "templates", "SCHEMA.md"))
	if templates.Schema != inRepo {
		t.Error("the embedded contract has drifted from templates/SCHEMA.md")
	}
}

func TestEnsureIsIdempotentAndNeverOverwrites(t *testing.T) {
	isolate(t, true)
	s := newStore(t, Options{})
	if _, err := s.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}

	// A user who edits the contract in their own store keeps their edit.
	schema := filepath.Join(s.Dir, "shared", "SCHEMA.md")
	if err := os.WriteFile(schema, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := s.Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.CreatedStore || report.InitialisedRepo || len(report.WroteTemplates) > 0 {
		t.Errorf("a second bootstrap did something: %+v", report)
	}
	if got := read(t, schema); got != "mine\n" {
		t.Errorf("SCHEMA.md is %q, want the user's own version", got)
	}
}

// A store created inside another repository must get its own, or the user's
// memory is committed into that project and `add -A` sweeps up their work.
func TestAStoreInsideAnotherRepositoryGetsItsOwn(t *testing.T) {
	isolate(t, true)
	outer := gitx.New(t.TempDir())
	if err := outer.Init(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outer.Dir, "code.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := outer.StageAll(); err != nil {
		t.Fatal(err)
	}
	if _, err := outer.Commit("the project"); err != nil {
		t.Fatal(err)
	}

	s := New(filepath.Join(outer.Dir, "memory"), Options{
		Locker: lock.New(filepath.Join(t.TempDir(), "locks")),
		Now:    func() time.Time { return fixedDay },
	})
	if _, err := s.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !s.Repo().IsOwnRepo() {
		t.Fatal("the store did not get a repository of its own")
	}

	writeSomething(t, s)
	if _, err := s.Commit(t.Context(), "save"); err != nil {
		t.Fatal(err)
	}

	log, _ := outer.Try("log", "--oneline")
	if strings.Count(strings.TrimSpace(log), "\n") != 0 {
		t.Errorf("the outer project has more than its own commit:\n%s", log)
	}
	if files, _ := outer.Try("show", "--name-only", "--format=", "HEAD"); strings.Contains(files, "memory/") {
		t.Errorf("the store's files landed in the outer project: %s", files)
	}
}

func TestWiringAHubWithNothingOnIt(t *testing.T) {
	isolate(t, true)
	hub := newHub(t)
	s := newStore(t, Options{Remote: hub})

	report, err := s.Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.WiredRemote != hub {
		t.Errorf("wired %q, want the hub", report.WiredRemote)
	}
	if report.AdoptedHistory {
		t.Error("it adopted history from a hub that has none")
	}
	if got := s.Repo().Status().Remote; got != hub {
		t.Errorf("remote is %q, want the hub", got)
	}
}

// The second machine finds the memory already on the hub instead of starting
// empty. This is what makes one store follow a person between computers.
func TestASecondMachineAdoptsWhatIsOnTheHub(t *testing.T) {
	isolate(t, true)
	hub := newHub(t)

	first := newStore(t, Options{Remote: hub})
	if _, err := first.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	writeSomething(t, first)
	if _, err := first.Commit(t.Context(), "save"); err != nil {
		t.Fatal(err)
	}
	if got := first.Repo().Push(""); !got.Pushed {
		t.Fatalf("publishing from the first machine: %+v", got)
	}

	second := newStore(t, Options{Remote: hub})
	report, err := second.Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !report.AdoptedHistory {
		t.Fatalf("the second machine did not adopt the hub's memory: %+v", report)
	}
	if _, ok := memory.ReadProject(second.Dir, "a-project"); !ok {
		t.Error("the project from the first machine is not here")
	}
	if report.AdoptionBlocked != "" {
		t.Errorf("adoption was reported as blocked: %s", report.AdoptionBlocked)
	}
}

// Two memories that never met must not be merged by a side effect of a save.
func TestAHubNeverOverwritesAStoreThatHasContent(t *testing.T) {
	isolate(t, true)
	hub := newHub(t)

	first := newStore(t, Options{Remote: hub})
	if _, err := first.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	writeSomething(t, first)
	if _, err := first.Commit(t.Context(), "save"); err != nil {
		t.Fatal(err)
	}
	if got := first.Repo().Push(""); !got.Pushed {
		t.Fatal("publishing from the first machine")
	}

	second := newStore(t, Options{Remote: hub})
	if err := os.MkdirAll(memory.SharedDir(second.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(memory.SharedDir(second.Dir), "SCHEMA.md")
	if err := os.WriteFile(mine, []byte("what this machine already had\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := second.Ensure(t.Context())
	if err != nil {
		t.Fatalf("bootstrap failed instead of explaining: %v", err)
	}
	if report.AdoptedHistory {
		t.Fatal("it adopted the hub over content that was already here")
	}
	if !strings.Contains(report.AdoptionBlocked, "would be overwritten") {
		t.Errorf("explanation is %q, want it to say why", report.AdoptionBlocked)
	}
	if got := read(t, mine); got != "what this machine already had\n" {
		t.Errorf("the local file was replaced: %q", got)
	}
	// The store still works, which is the point of not failing.
	writeSomething(t, second)
}

func TestAnUnreachableHubDoesNotBlockTheStore(t *testing.T) {
	isolate(t, true)
	s := newStore(t, Options{Remote: filepath.Join(t.TempDir(), "nowhere.git")})

	report, err := s.Ensure(t.Context())
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if report.AdoptedHistory {
		t.Error("it adopted something from a hub that is not there")
	}
	writeSomething(t, s)
}

func TestEnsureReportsAMissingIdentity(t *testing.T) {
	isolate(t, false)
	s := newStore(t, Options{})

	report, err := s.Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.HasIdentity {
		t.Errorf("identity is %+v, want none: the test is reading the machine's git config", report.Identity)
	}
}

func TestBootstrapClearsAbandonedTemporaryFiles(t *testing.T) {
	isolate(t, true)
	s := newStore(t, Options{})
	if _, err := s.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}

	dir := memory.MemoriesDir(s.Dir)
	stale := filepath.Join(dir, ".one.md"+tempPrefix+"abcdef")
	fresh := filepath.Join(dir, ".two.md"+tempPrefix+"123456")
	for _, path := range []string{stale, fresh} {
		if err := os.WriteFile(path, []byte("half a file"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := fixedDay.Add(-2 * time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("an abandoned temporary file was left behind")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a temporary file that may belong to a running write was removed")
	}
}

// writeSomething puts one project and one memory in a store, for tests that need
// a store with content rather than a particular content.
func writeSomething(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.UpsertProject(t.Context(), ProjectInput{Slug: "a-project", Name: "A Project"}); err != nil {
		t.Fatalf("creating a project: %v", err)
	}
	if _, err := s.WriteMemory(t.Context(), MemoryInput{
		ID:       "a-fact",
		Projects: []string{"a-project"},
		Type:     "decision",
		Body:     "Something was decided.",
	}); err != nil {
		t.Fatalf("writing a memory: %v", err)
	}
}
