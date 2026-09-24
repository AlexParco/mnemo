package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// isolate cuts the test off from the machine it runs on. Without this, the
// developer's own git identity and configuration leak in, and the test that
// proves mnemo reports a missing identity would pass for the wrong reason.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_DIR", "GIT_WORK_TREE",
	} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
}

// newRepo is an initialised store with no committer identity yet.
func newRepo(t *testing.T) *Repo {
	t.Helper()
	isolate(t)
	repo := New(t.TempDir())
	if err := repo.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	return repo
}

func withIdentity(t *testing.T, repo *Repo) *Repo {
	t.Helper()
	for _, kv := range [][2]string{{"user.name", "Test"}, {"user.email", "test@example.invalid"}} {
		if _, err := repo.Run("config", kv[0], kv[1]); err != nil {
			t.Fatalf("setting %s: %v", kv[0], err)
		}
	}
	return repo
}

func write(t *testing.T, repo *Repo, name, content string) {
	t.Helper()
	path := filepath.Join(repo.Dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, repo *Repo, message string) string {
	t.Helper()
	if err := repo.StageAll(); err != nil {
		t.Fatalf("staging: %v", err)
	}
	sha, err := repo.Commit(message)
	if err != nil {
		t.Fatalf("committing: %v", err)
	}
	return sha
}

func TestTryAnswersOrSaysItCannot(t *testing.T) {
	repo := newRepo(t)

	if out, ok := repo.Try("symbolic-ref", "--short", "HEAD"); !ok || out != "main" {
		t.Errorf("Try on a question gave (%q, %v), want the branch", out, ok)
	}
	// A store with no commits has an unborn branch, which is the state right
	// after a bootstrap. HEAD does not resolve there, and neither does
	// `rev-parse --abbrev-ref HEAD`: that is why Status asks symbolic-ref first
	// and only falls back to rev-parse.
	if out, ok := repo.Try("rev-parse", "-q", "--verify", "HEAD"); ok {
		t.Errorf("Try gave %q for a HEAD that does not exist, want it to say it cannot answer", out)
	}
	if out, ok := repo.Try("rev-parse", "--abbrev-ref", "HEAD"); ok {
		t.Errorf("Try gave %q for the branch of an unborn HEAD, want it to say it cannot answer", out)
	}
	if _, ok := New(filepath.Join(repo.Dir, "nowhere")).Try("status"); ok {
		t.Error("Try answered for a directory that does not exist")
	}
}

func TestRunCarriesGitsExplanation(t *testing.T) {
	repo := newRepo(t)

	_, err := repo.Run("checkout", "no-such-branch")
	var gitErr *Error
	if !errors.As(err, &gitErr) {
		t.Fatalf("Run gave %v (%T), want a git error", err, err)
	}
	if !strings.Contains(gitErr.Error(), "checkout") {
		t.Errorf("the message does not name the command: %s", gitErr)
	}
	if gitErr.Stderr == "" {
		t.Error("the error carries no stderr, so nobody can tell what git objected to")
	}
}

func TestAttemptReturnsBothStreams(t *testing.T) {
	repo := withIdentity(t, newRepo(t))
	write(t, repo, "note.md", "one\n")
	commit(t, repo, "first")

	if got := repo.Attempt("status", "--porcelain"); !got.OK {
		t.Errorf("a command that works gave %+v", got)
	}
	failed := repo.Attempt("rebase", "--continue")
	if failed.OK {
		t.Error("a rebase with nothing to continue reported success")
	}
	if failed.Detail() == "" {
		t.Error("a failed attempt says nothing about why")
	}
}

// Git must never stop to ask a person something: no editor, no credential
// prompt. There is no terminal here, and a prompt would hang the agent waiting
// for the answer.
func TestGitNeverWaitsForAPerson(t *testing.T) {
	repo := newRepo(t)

	if editor, _ := repo.Try("var", "GIT_EDITOR"); editor != "true" {
		t.Errorf("GIT_EDITOR is %q, want a command that exits at once", editor)
	}
	if !slices.Contains(repo.env(), "GIT_TERMINAL_PROMPT=0") {
		t.Error("git is allowed to ask for credentials")
	}
}

// A store created inside another repository must get its own, or every commit
// lands in the enclosing project and `add -A` sweeps up the user's own work.
func TestIsOwnRepo(t *testing.T) {
	outer := withIdentity(t, newRepo(t))
	write(t, outer, "README.md", "outer\n")
	commit(t, outer, "outer")

	inner := New(filepath.Join(outer.Dir, "memory"))
	if err := os.MkdirAll(inner.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if !outer.IsOwnRepo() {
		t.Error("the outer repository does not recognise itself")
	}
	if inner.IsOwnRepo() {
		t.Error("a directory inside another repository claims to be its own")
	}
	if got := inner.Status(); got.IsRepo {
		t.Errorf("status of a directory inside another repository is %+v, want not a repository", got)
	}

	if err := inner.Init(); err != nil {
		t.Fatal(err)
	}
	if !inner.IsOwnRepo() {
		t.Error("after init, the inner store still does not own its repository")
	}

	plain := New(t.TempDir())
	if plain.IsOwnRepo() {
		t.Error("a directory with no repository claims to have one")
	}
}

func TestStatusThroughTheStatesOfAStore(t *testing.T) {
	repo := withIdentity(t, newRepo(t))

	fresh := repo.Status()
	if !fresh.IsRepo || fresh.Branch != "main" {
		t.Errorf("a fresh store is %+v, want an empty repository on main", fresh)
	}
	if fresh.HasRemote() || fresh.Dirty || fresh.Rebasing {
		t.Errorf("a fresh store is %+v, want nothing wired and nothing pending", fresh)
	}
	if fresh.UnpushedKnown {
		t.Error("a store that tracks nothing must not claim to know how much is unpushed")
	}

	write(t, repo, "memories/one.md", "---\nid: one\n---\nbody\n")
	if !repo.Status().Dirty {
		t.Error("a written file did not make the store dirty")
	}
	commit(t, repo, "save one")
	if repo.Status().Dirty {
		t.Error("the store is still dirty after committing everything")
	}

	// A rebase in progress is the state where nothing else may run. git keeps it
	// in a directory of its own, which is what the check looks for.
	gitDir := filepath.Join(repo.Dir, ".git", "rebase-merge")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !repo.Status().Rebasing {
		t.Error("a rebase in progress went unnoticed")
	}
	if err := os.RemoveAll(gitDir); err != nil {
		t.Fatal(err)
	}
}

func TestStatusCountsWhatTheHubHasNotSeen(t *testing.T) {
	repo := withIdentity(t, newRepo(t))
	write(t, repo, "one.md", "one\n")
	commit(t, repo, "first")

	hub := New(t.TempDir())
	if _, err := hub.Run("init", "-q", "--bare"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Run("remote", "add", "origin", hub.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Run("push", "-q", "-u", "origin", "main"); err != nil {
		t.Fatal(err)
	}

	published := repo.Status()
	if published.Remote != hub.Dir {
		t.Errorf("remote is %q, want the hub", published.Remote)
	}
	if !published.UnpushedKnown || published.Unpushed != 0 {
		t.Errorf("just after a push the count is %+v, want a known zero", published)
	}

	write(t, repo, "two.md", "two\n")
	commit(t, repo, "second")
	if got := repo.Status(); !got.UnpushedKnown || got.Unpushed != 1 {
		t.Errorf("after one local commit the count is %+v, want a known one", got)
	}
}

func TestIdentity(t *testing.T) {
	repo := newRepo(t)

	if id, ok := repo.Identity(); ok {
		t.Errorf("an isolated store has identity %+v, want none: the test is reading the machine's git config", id)
	}
	if _, err := repo.Run("config", "user.name", "Test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.Identity(); ok {
		t.Error("half an identity counted as one; a commit would still fail")
	}

	withIdentity(t, repo)
	id, ok := repo.Identity()
	if !ok || id.Name != "Test" || id.Email != "test@example.invalid" {
		t.Errorf("identity is %+v (%v), want what was configured", id, ok)
	}
}

func TestStagingAndCommitting(t *testing.T) {
	repo := withIdentity(t, newRepo(t))
	write(t, repo, "a.md", "a\n")
	write(t, repo, "b.md", "b\n")

	if got := repo.StagedFiles(); got != nil {
		t.Errorf("nothing is staged yet, but StagedFiles says %q", got)
	}
	if err := repo.Stage("a.md"); err != nil {
		t.Fatal(err)
	}
	if got := repo.StagedFiles(); !slices.Equal(got, []string{"a.md"}) {
		t.Errorf("staged %q, want only the file that was staged", got)
	}
	if err := repo.StageAll(); err != nil {
		t.Fatal(err)
	}
	if got := repo.StagedFiles(); !slices.Equal(got, []string{"a.md", "b.md"}) {
		t.Errorf("staged %q, want both files", got)
	}

	sha, err := repo.Commit("save")
	if err != nil {
		t.Fatalf("committing: %v", err)
	}
	if len(sha) < 7 {
		t.Errorf("commit returned %q, want a short commit id", sha)
	}
	if got := repo.StagedFiles(); got != nil {
		t.Errorf("after the commit, %q is still staged", got)
	}
	if !repo.HasCommits() {
		t.Error("the store says it has no commits right after one")
	}
	if got := repo.ConflictedFiles(); got != nil {
		t.Errorf("a store that never merged reports conflicts: %q", got)
	}
}

func TestCommitWithoutIdentityFails(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "a.md", "a\n")
	if err := repo.StageAll(); err != nil {
		t.Fatal(err)
	}

	_, err := repo.Commit("save")
	var gitErr *Error
	if !errors.As(err, &gitErr) {
		t.Fatalf("committing with no identity gave %v, want a git error the caller can explain", err)
	}
}
