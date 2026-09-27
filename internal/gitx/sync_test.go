package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run against a real hub: a bare repository and two clones of it,
// which is what two of the user's machines are. Nothing here is simulated.

func newHub(t *testing.T) *Repo {
	t.Helper()
	hub := New(t.TempDir())
	if _, err := hub.Run("init", "-q", "--bare"); err != nil {
		t.Fatalf("creating the hub: %v", err)
	}
	return hub
}

func newClone(t *testing.T, hub *Repo) *Repo {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	if _, err := New(t.TempDir()).Run("clone", "-q", hub.Dir, dir); err != nil {
		t.Fatalf("cloning: %v", err)
	}
	return withIdentity(t, New(dir))
}

// firstMachine is a store wired to a hub with one commit already published.
func firstMachine(t *testing.T, hub *Repo, name, content string) *Repo {
	t.Helper()
	repo := withIdentity(t, newRepo(t))
	write(t, repo, name, content)
	commit(t, repo, "first")
	if _, err := repo.Run("remote", "add", "origin", hub.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Run("push", "-q", "-u", "origin", "main"); err != nil {
		t.Fatal(err)
	}
	return repo
}

func read(t *testing.T, repo *Repo, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repo.Dir, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(content)
}

func TestPullWithoutAHub(t *testing.T) {
	repo := withIdentity(t, newRepo(t))

	got := repo.Pull()
	if got.HasRemote || got.Pulled {
		t.Errorf("pull gave %+v, want nothing to sync with", got)
	}
	if !strings.Contains(got.Detail, "no remote") {
		t.Errorf("detail is %q, want it to say the store has no remote", got.Detail)
	}
}

func TestPullBeforeTheFirstPush(t *testing.T) {
	hub := newHub(t)
	repo := withIdentity(t, newRepo(t))
	write(t, repo, "note.md", "one\n")
	commit(t, repo, "first")
	if _, err := repo.Run("remote", "add", "origin", hub.Dir); err != nil {
		t.Fatal(err)
	}

	got := repo.Pull()
	if !got.HasRemote || got.Pulled {
		t.Errorf("pull gave %+v, want a remote that is wired but not tracked", got)
	}
	if !strings.Contains(got.Detail, "first push") {
		t.Errorf("detail is %q, want it to say the first push sets tracking up", got.Detail)
	}
}

func TestPullBringsTheOtherMachinesWork(t *testing.T) {
	hub := newHub(t)
	first := firstMachine(t, hub, "memories/one.md", "one\n")
	second := newClone(t, hub)

	write(t, first, "memories/two.md", "two\n")
	commit(t, first, "save two")
	if got := first.Push(""); !got.Pushed {
		t.Fatalf("publishing from the first machine: %+v", got)
	}

	got := second.Pull()
	if !got.Pulled || len(got.Conflicts) > 0 {
		t.Fatalf("pull gave %+v, want it to bring the work in", got)
	}
	if read(t, second, "memories/two.md") != "two\n" {
		t.Error("the second machine did not receive the file")
	}

	if again := second.Pull(); !again.Pulled || !strings.Contains(again.Detail, "up to date") {
		t.Errorf("a second pull gave %+v, want it to say there is nothing new", again)
	}
}

// pending.md is the file two machines really collide on: both of them are
// working on the same project and both update what is left to do.
func conflicted(t *testing.T) (*Repo, *Repo) {
	t.Helper()
	hub := newHub(t)
	base := "# Pending\n\n## Next\n- [ ] the shared line\n"
	first := firstMachine(t, hub, "projects/app/pending.md", base)
	second := newClone(t, hub)

	write(t, first, "projects/app/pending.md", "# Pending\n\n## Next\n- [ ] what the first machine added\n")
	commit(t, first, "first machine")
	if got := first.Push(""); !got.Pushed {
		t.Fatalf("publishing from the first machine: %+v", got)
	}

	write(t, second, "projects/app/pending.md", "# Pending\n\n## Next\n- [ ] what the second machine added\n")
	commit(t, second, "second machine")
	return first, second
}

func TestAConflictIsReportedRatherThanGuessed(t *testing.T) {
	_, second := conflicted(t)

	got := second.Pull()
	if got.Pulled {
		t.Fatal("the pull claimed to succeed through a conflict")
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0].File != "projects/app/pending.md" {
		t.Fatalf("conflicts are %+v, want the pending file", got.Conflicts)
	}
	if !strings.Contains(got.Conflicts[0].Content, "<<<<<<<") {
		t.Error("the conflict came back without the markers, so the agent cannot see both sides")
	}
	if !got.Rebasing {
		t.Error("the store is mid-rebase and does not say so")
	}
}

func TestResolvingWithTheUnionKeepsBothSides(t *testing.T) {
	_, second := conflicted(t)
	second.Pull()

	union := "# Pending\n\n## Next\n- [ ] what the first machine added\n- [ ] what the second machine added\n"
	remaining, err := second.ResolveConflict("projects/app/pending.md", union)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("still conflicted: %q", remaining)
	}

	done := second.Rebase(Continue)
	if !done.OK || done.Rebasing {
		t.Fatalf("continuing gave %+v, want the rebase finished", done)
	}
	content := read(t, second, "projects/app/pending.md")
	if !strings.Contains(content, "first machine") || !strings.Contains(content, "second machine") {
		t.Errorf("the merged file lost a side:\n%s", content)
	}
	if second.Status().Dirty {
		t.Error("the store is dirty after a finished rebase")
	}
}

func TestResolveRefusesWhatWouldMakeThingsWorse(t *testing.T) {
	_, second := conflicted(t)
	second.Pull()

	halfMerged := "# Pending\n\n<<<<<<< HEAD\n- [ ] one\n=======\n- [ ] two\n>>>>>>> theirs\n"
	if _, err := second.ResolveConflict("projects/app/pending.md", halfMerged); !errors.Is(err, ErrConflictMarkers) {
		t.Errorf("resolving with markers gave %v, want a refusal", err)
	}
	if _, err := second.ResolveConflict("../outside.md", "anything\n"); !errors.Is(err, ErrOutsideRepo) {
		t.Errorf("resolving outside the store gave %v, want a refusal", err)
	}
	if _, err := second.ResolveConflict("projects/../../outside.md", "anything\n"); !errors.Is(err, ErrOutsideRepo) {
		t.Errorf("a path that climbs out gave %v, want a refusal", err)
	}
}

func TestContinueIsRefusedWhileAnythingIsUnresolved(t *testing.T) {
	_, second := conflicted(t)
	second.Pull()

	got := second.Rebase(Continue)
	if got.OK {
		t.Fatal("the rebase continued with a file still conflicted")
	}
	if len(got.Remaining) != 1 {
		t.Errorf("remaining is %q, want the one unresolved file", got.Remaining)
	}
	if !got.Rebasing {
		t.Error("the store is still mid-rebase and does not say so")
	}
	// git would refuse this by itself, and its refusal is a wall of text about
	// rebases. The wording here is the whole point: it names the file and says
	// what to do about it. Without mnemo's own check, this is what is lost.
	want := "Still unresolved: projects/app/pending.md. Resolve them before continuing."
	if got.Detail != want {
		t.Errorf("detail is %q, want %q", got.Detail, want)
	}
}

func TestAbortPutsTheStoreBack(t *testing.T) {
	_, second := conflicted(t)
	before := read(t, second, "projects/app/pending.md")
	second.Pull()

	got := second.Rebase(Abort)
	if !got.OK || got.Rebasing {
		t.Fatalf("aborting gave %+v, want the rebase undone", got)
	}
	if after := read(t, second, "projects/app/pending.md"); after != before {
		t.Errorf("the file is not what it was:\n got: %q\nwant: %q", after, before)
	}
}

func TestRebaseWithNothingToFinish(t *testing.T) {
	repo := withIdentity(t, newRepo(t))
	got := repo.Rebase(Continue)
	if got.OK || !strings.Contains(got.Detail, "No rebase") {
		t.Errorf("continuing with no rebase gave %+v, want it to say so", got)
	}
}

func TestPushPublishesOnceAndThenHasNothingToSay(t *testing.T) {
	hub := newHub(t)
	repo := withIdentity(t, newRepo(t))
	write(t, repo, "memories/one.md", "one\n")
	commit(t, repo, "save one")
	if _, err := repo.Run("remote", "add", "origin", hub.Dir); err != nil {
		t.Fatal(err)
	}

	first := repo.Push("")
	if !first.Pushed || first.Commits != 1 {
		t.Fatalf("the first push gave %+v, want one commit published", first)
	}
	if _, ok := repo.Upstream(); !ok {
		t.Error("the first push did not set the branch up to track the hub")
	}

	again := repo.Push("")
	if again.Pushed || again.Refused != RefusedNothingToPush {
		t.Errorf("a second push gave %+v, want it to say there is nothing left", again)
	}
	if !strings.Contains(again.Detail, "already on the hub") {
		t.Errorf("detail is %q", again.Detail)
	}
}

func TestPushRefusals(t *testing.T) {
	t.Run("a store with no remote", func(t *testing.T) {
		repo := withIdentity(t, newRepo(t))
		write(t, repo, "one.md", "one\n")
		commit(t, repo, "save")
		if got := repo.Push(""); got.Refused != RefusedNoRemote || got.Pushed {
			t.Errorf("push gave %+v, want it refused for having no remote", got)
		}
	})

	t.Run("a store with no commits", func(t *testing.T) {
		hub := newHub(t)
		repo := withIdentity(t, newRepo(t))
		if _, err := repo.Run("remote", "add", "origin", hub.Dir); err != nil {
			t.Fatal(err)
		}
		if got := repo.Push(""); got.Refused != RefusedNothingToPush {
			t.Errorf("push gave %+v, want it refused for having nothing to publish", got)
		}
	})

	t.Run("a store mid-rebase", func(t *testing.T) {
		_, second := conflicted(t)
		second.Pull()
		got := second.Push("")
		if got.Refused != RefusedRebasing || got.Pushed {
			t.Errorf("push gave %+v, want it refused while the store is mid-merge", got)
		}
	})
}

func TestASecretStopsThePushAndOnlyTheRightWordLetsItThrough(t *testing.T) {
	hub := newHub(t)
	repo := firstMachine(t, hub, "memories/one.md", "one\n")

	write(t, repo, "memories/leak.md", "aws_key = "+awsKey+"\n")
	commit(t, repo, "save the leak")

	refused := repo.Push("")
	if refused.Pushed || refused.Refused != RefusedSecrets {
		t.Fatalf("push gave %+v, want it stopped by the scan", refused)
	}
	if len(refused.Findings) != 1 || refused.Findings[0].File != "memories/leak.md" {
		t.Errorf("findings are %+v, want the file that carries it", refused.Findings)
	}
	if refused.Acknowledge == "" {
		t.Error("the refusal gives no way to say it is a false positive")
	}
	if strings.Contains(refused.Findings[0].Excerpt, awsKey) {
		t.Error("the refusal repeats the secret it is reporting")
	}
	// Nothing left the machine.
	if out, _ := hub.Try("log", "--oneline"); strings.Contains(out, "save the leak") {
		t.Fatal("the commit reached the hub anyway")
	}

	wrong := repo.Push("0123456789ab")
	if wrong.Pushed || wrong.Refused != RefusedBadAcknowledge {
		t.Errorf("a wrong acknowledgement gave %+v, want it refused", wrong)
	}

	right := repo.Push(refused.Acknowledge)
	if !right.Pushed {
		t.Fatalf("the acknowledgement that was issued did not let it through: %+v", right)
	}
	if out, _ := hub.Try("log", "--oneline"); !strings.Contains(out, "save the leak") {
		t.Error("the push reported success but the hub does not have it")
	}
}

func TestTheScanRunsOnAFirstPushToo(t *testing.T) {
	hub := newHub(t)
	repo := withIdentity(t, newRepo(t))
	write(t, repo, "memories/leak.md", "aws_key = "+awsKey+"\n")
	commit(t, repo, "save")
	if _, err := repo.Run("remote", "add", "origin", hub.Dir); err != nil {
		t.Fatal(err)
	}

	// There is no upstream yet, so there is nothing to compare against. The scan
	// has to read the whole history instead of waving it through.
	if got := repo.Push(""); got.Pushed || got.Refused != RefusedSecrets {
		t.Errorf("the first push gave %+v, want the scan to stop it", got)
	}
}

// A symlink inside the store defeats a check made on the path alone: the path
// stays inside, and the write follows the link out.
//
// What actually stops it today is the conflicted-file check, which comes first,
// so the symlink guard behind it has no control of its own — see
// docs/specs/testing.md. Both are kept: git tracks symlinks, so one can arrive
// from the hub, and the day the first check changes shape this is the one left
// standing.
func TestResolveConflictWillNotFollowASymlinkOutOfTheStore(t *testing.T) {
	repo := withIdentity(t, newRepo(t))
	write(t, repo, "memories/a.md", "a\n")
	commit(t, repo, "save: one")

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo.Dir, "link")); err != nil {
		t.Skipf("this filesystem has no symlinks: %v", err)
	}

	_, err := repo.ResolveConflict("link/through-a-symlink.md", "written out of bounds\n")
	if err == nil {
		t.Fatal("a write through a symlink was accepted")
	}
	if !errors.Is(err, ErrOutsideRepo) && !errors.Is(err, ErrNotConflicted) {
		t.Errorf("the error is %v, want one that names the reason", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "through-a-symlink.md")); !os.IsNotExist(statErr) {
		t.Error("the file was written outside the store")
	}
}
