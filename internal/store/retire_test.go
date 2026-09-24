package store

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// published is a store wired to a hub with everything pushed, which is the only
// state in which retiring is not destroying the last copy.
func published(t *testing.T) (*Store, string) {
	t.Helper()
	isolate(t, true)
	hub := newHub(t)
	s := newStore(t, Options{Remote: hub})
	writeSomething(t, s)
	if _, err := s.Commit(t.Context(), "save: something"); err != nil {
		t.Fatal(err)
	}
	if got := s.Repo().Push(""); !got.Pushed {
		t.Fatalf("publishing: %+v", got)
	}
	return s, hub
}

// fingerprint is a checksum of every file under a directory, for proving that
// nothing in it changed.
func fingerprint(t *testing.T, dir string) string {
	t.Helper()
	sum := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum.Write([]byte(rel))
		sum.Write(content)
		return nil
	})
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// writeAnother adds one more memory, for tests that need the store to change
// after a plan was made.
func writeAnother(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.WriteMemory(t.Context(), MemoryInput{
		ID:       id,
		Projects: []string{"a-project"},
		Type:     "decision",
		Body:     "Something else was decided.",
	}); err != nil {
		t.Fatalf("writing %s: %v", id, err)
	}
}

func TestRetirePlanCountsWhatWouldGo(t *testing.T) {
	s, hub := published(t)

	plan, err := s.PlanRetire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Exists || plan.Store != s.Dir {
		t.Errorf("plan = %+v, want it to name the store", plan)
	}
	if len(plan.Projects) != 1 || plan.Projects[0].Slug != "a-project" || plan.Projects[0].Memories != 1 {
		t.Errorf("projects = %+v, want each one named with its count", plan.Projects)
	}
	if plan.Memories != 1 || plan.SharedFiles != 1 {
		t.Errorf("totals are %d memories and %d shared files", plan.Memories, plan.SharedFiles)
	}
	if plan.Remote != hub || plan.Dirty || plan.AtRisk() {
		t.Errorf("plan = %+v, want a published store with nothing at risk", plan)
	}

	if _, err := os.Stat(s.Dir); err != nil {
		t.Error("planning removed the store")
	}
}

func TestRetireMovesTheStoreAside(t *testing.T) {
	s, hub := published(t)
	before := fingerprint(t, hub)

	plan, err := s.PlanRetire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ApplyRetire(t.Context(), plan.Confirm, RetireOptions{})
	if err != nil {
		t.Fatalf("retiring: %v", err)
	}

	if got.Purged || got.MovedTo == "" {
		t.Fatalf("result = %+v, want the store moved aside", got)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Error("the store is still where it was")
	}
	// Everything is in the copy, not only the files mnemo happens to know about.
	for _, rel := range []string{
		"shared/SCHEMA.md", ".gitignore", "memories/a-fact.md", "projects/a-project/INDEX.md", ".git/HEAD",
	} {
		if _, err := os.Stat(filepath.Join(got.MovedTo, rel)); err != nil {
			t.Errorf("%s is missing from the retired copy", rel)
		}
	}

	// The hub is the copy that outlives the machine, and nothing here touches it.
	if after := fingerprint(t, hub); after != before {
		t.Error("the hub changed; retiring must never reach it")
	}
	if got.Remote != hub {
		t.Errorf("the result does not name the hub that still has the memory: %+v", got)
	}

	if listed := s.RetiredStores(); len(listed) != 1 || listed[0] != got.MovedTo {
		t.Errorf("retired stores = %q, want the one just made", listed)
	}
}

func TestRetiringTwiceInADayGetsACounter(t *testing.T) {
	s, _ := published(t)
	plan, err := s.PlanRetire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ApplyRetire(t.Context(), plan.Confirm, RetireOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// A fresh store in the same place, retired again on the same day.
	writeAnother(t, s, "another-fact")
	if _, err := s.Commit(t.Context(), "save: the new one"); err != nil {
		t.Fatal(err)
	}
	plan, err = s.PlanRetire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ApplyRetire(t.Context(), plan.Confirm, RetireOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}

	if second.MovedTo == first.MovedTo {
		t.Fatalf("the second retirement overwrote the first at %s", first.MovedTo)
	}
	if !strings.HasSuffix(second.MovedTo, ".2") {
		t.Errorf("the second copy is at %s, want a counter", second.MovedTo)
	}
	if _, err := os.Stat(filepath.Join(first.MovedTo, "memories", "a-fact.md")); err != nil {
		t.Error("the first retired copy was damaged")
	}
}

func TestPurgeLeavesNothing(t *testing.T) {
	s, _ := published(t)
	plan, err := s.PlanRetire(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ApplyRetire(t.Context(), plan.Confirm, RetireOptions{Purge: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Purged || got.MovedTo != "" {
		t.Errorf("result = %+v, want it deleted outright", got)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Error("the store is still there")
	}
	if listed := s.RetiredStores(); len(listed) != 0 {
		t.Errorf("purging left copies behind: %q", listed)
	}
}

// Retiring a store whose memory exists nowhere else is the one case worth
// stopping, and the only one the user can override.
func TestRetireRefusesWhileThisIsTheOnlyCopy(t *testing.T) {
	t.Run("nothing was ever pushed", func(t *testing.T) {
		isolate(t, true)
		s := newStore(t, Options{})
		writeSomething(t, s)
		if _, err := s.Commit(t.Context(), "save"); err != nil {
			t.Fatal(err)
		}

		plan, err := s.PlanRetire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !plan.AtRisk() {
			t.Error("a store with no hub does not know it is the only copy")
		}
		_, err = s.ApplyRetire(t.Context(), plan.Confirm, RetireOptions{})
		if !IsRefusal(err) {
			t.Fatalf("got %v, want a refusal", err)
		}
		if !strings.Contains(err.Error(), "no hub") {
			t.Errorf("the message is %q", err)
		}
		if _, statErr := os.Stat(s.Dir); statErr != nil {
			t.Error("it was retired anyway")
		}

		// The user can still decide, and say so.
		if _, err := s.ApplyRetire(t.Context(), plan.Confirm, RetireOptions{Force: true}); err != nil {
			t.Fatalf("forcing: %v", err)
		}
		if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
			t.Error("forcing did not retire it")
		}
	})

	t.Run("commits the hub has not seen", func(t *testing.T) {
		s, _ := published(t)
		writeAnother(t, s, "not-pushed-yet")
		if _, err := s.Commit(t.Context(), "save: not pushed"); err != nil {
			t.Fatal(err)
		}

		plan, err := s.PlanRetire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApplyRetire(t.Context(), plan.Confirm, RetireOptions{}); !IsRefusal(err) {
			t.Fatalf("got %v, want a refusal", err)
		}
	})

	t.Run("work that is not even committed", func(t *testing.T) {
		s, _ := published(t)
		if err := os.WriteFile(filepath.Join(s.Dir, "memories", "draft.md"), []byte("---\nid: draft\n---\n\nHalf written.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		plan, err := s.PlanRetire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.ApplyRetire(t.Context(), plan.Confirm, RetireOptions{})
		if !IsRefusal(err) {
			t.Fatalf("got %v, want a refusal", err)
		}
		if !strings.Contains(err.Error(), "not even committed") {
			t.Errorf("the message is %q", err)
		}
	})
}

func TestRetireNeedsTheConfirmationFromItsOwnPlan(t *testing.T) {
	s, _ := published(t)
	plan, err := s.PlanRetire(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.ApplyRetire(t.Context(), "000000000000", RetireOptions{}); !IsRefusal(err) {
		t.Fatalf("a wrong confirmation gave %v, want a refusal", err)
	}

	// Anything that changes what would go invalidates the plan.
	writeAnother(t, s, "one-more-fact")
	if _, err := s.Commit(t.Context(), "save: more"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyRetire(t.Context(), plan.Confirm, RetireOptions{Force: true}); !IsRefusal(err) {
		t.Fatalf("a stale confirmation gave %v, want a refusal", err)
	}
	if _, err := os.Stat(s.Dir); err != nil {
		t.Error("the store was retired on a stale plan")
	}
}

// The lock file belongs to the store. Everything else in the state directory
// belongs to the machine and has to survive.
func TestRetireCleansItsLockAndNothingElse(t *testing.T) {
	s, _ := published(t)
	if _, err := s.PlanRetire(t.Context()); err != nil {
		t.Fatal(err)
	}
	locks := filepath.Dir(s.locker.File(s.Dir))
	mailbox := filepath.Join(locks, "not-a-lock-of-this-store")
	if err := os.WriteFile(mailbox, []byte("belongs to the machine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.locker.File(s.Dir)); err != nil {
		t.Fatalf("the store's lock file does not exist to begin with: %v", err)
	}

	plan, err := s.PlanRetire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyRetire(t.Context(), plan.Confirm, RetireOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(s.locker.File(s.Dir)); !os.IsNotExist(err) {
		t.Error("the store's lock file was left behind")
	}
	if _, err := os.Stat(mailbox); err != nil {
		t.Error("something that belongs to the machine was removed")
	}
}

func TestRetiringAStoreThatIsNotThere(t *testing.T) {
	isolate(t, true)
	s := newStore(t, Options{})
	if _, err := s.PlanRetire(t.Context()); !IsRefusal(err) {
		t.Errorf("got %v, want a refusal saying there is nothing to retire", err)
	}
}

// After retiring, the machine is not broken: the next save builds a fresh store
// and, with a hub set, finds what is on it.
func TestAfterRetiringTheNextSaveStartsAgain(t *testing.T) {
	s, hub := published(t)
	plan, err := s.PlanRetire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyRetire(t.Context(), plan.Confirm, RetireOptions{}); err != nil {
		t.Fatal(err)
	}

	fresh := New(s.Dir, Options{Locker: s.locker, Remote: hub, Now: s.now})
	report, err := fresh.Ensure(t.Context())
	if err != nil {
		t.Fatalf("starting again: %v", err)
	}
	if !report.AdoptedHistory {
		t.Errorf("the fresh store did not find the hub's memory: %+v", report)
	}
	if _, ok := findMemory(fresh.Dir, "a-fact"); !ok {
		t.Error("the memory that was on the hub is not back")
	}
}
