package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AlexParco/mnemo/internal/gitx"
	"github.com/AlexParco/mnemo/internal/memory"
	"github.com/AlexParco/mnemo/templates"
)

// Report is what a bootstrap did. Every field is something a person might need
// told: a store that was created, a hub that was wired, memory that was adopted
// from it, or a reason none of that happened.
type Report struct {
	Store           string
	CreatedStore    bool
	InitialisedRepo bool
	// WiredRemote is the hub added during this call, empty when there was
	// nothing to add or one was already there.
	WiredRemote string
	// AdoptedHistory is true when this store took on memory that was already on
	// the hub, which is what happens on a second machine.
	AdoptedHistory bool
	// AdoptionBlocked explains why the hub's memory could not be taken on.
	AdoptionBlocked string
	WroteTemplates  []string
	Identity        gitx.Identity
	HasIdentity     bool
}

// Ensure creates what is missing and never overwrites what is there. It is safe
// to call before every write, which is what the write operations do.
func (s *Store) Ensure(ctx context.Context) (Report, error) {
	var report Report
	err := s.hold(ctx, func() error {
		var err error
		report, err = s.ensure()
		return err
	})
	return report, err
}

// ensure assumes the lock is held.
//
// The order is load-bearing. On a second machine pointed at a hub that already
// has memory, writing .gitignore or shared/SCHEMA.md before adopting would leave
// untracked files in the way of the checkout, and the adoption would fail.
func (s *Store) ensure() (Report, error) {
	report := Report{Store: s.Dir}

	if _, err := os.Stat(s.Dir); os.IsNotExist(err) {
		report.CreatedStore = true
	}
	for _, sub := range []string{"projects", "memories", "shared"} {
		if err := os.MkdirAll(filepath.Join(s.Dir, sub), 0o755); err != nil {
			return report, fmt.Errorf("creating the store: %w", err)
		}
	}

	if !s.repo.IsOwnRepo() {
		if err := s.repo.Init(); err != nil {
			return report, err
		}
		report.InitialisedRepo = true
	}

	if err := s.wireRemote(&report); err != nil {
		return report, err
	}

	if err := s.writeTemplates(&report); err != nil {
		return report, err
	}

	report.Identity, report.HasIdentity = s.repo.Identity()

	now := s.now()
	cleanTemporaries(memory.MemoriesDir(s.Dir), now)
	cleanTemporaries(memory.SharedDir(s.Dir), now)
	for _, slug := range memory.ProjectSlugs(s.Dir) {
		cleanTemporaries(memory.ProjectDir(s.Dir, slug), now)
	}
	return report, nil
}

// wireRemote points the store at the hub, and takes on what is already there
// when this store has nothing of its own.
func (s *Store) wireRemote(report *Report) error {
	if s.remote == "" {
		return nil
	}
	if _, ok := s.repo.Try("remote", "get-url", "origin"); ok {
		return nil
	}
	if _, err := s.repo.Run("remote", "add", "origin", s.remote); err != nil {
		return err
	}
	report.WiredRemote = s.remote

	// An unreachable hub must not stop a local store from working.
	if _, ok := s.repo.Try("fetch", "-q", "origin"); !ok {
		return nil
	}
	if _, ok := s.repo.Try("rev-parse", "-q", "--verify", "origin/main"); !ok {
		return nil
	}
	if s.repo.HasCommits() {
		return nil
	}

	if _, err := s.repo.Run("checkout", "-q", "-B", "main", "--track", "origin/main"); err != nil {
		// Adoption is for a store with nothing in it. This one has local files
		// that the hub's history would overwrite, and choosing which memory
		// survives is the user's decision, not a side effect of a save. Wiring
		// the remote still stands, and the store stays usable.
		report.AdoptionBlocked = fmt.Sprintf(
			"The hub at %s already has memory, but this store has local content that would be overwritten, "+
				"so the two were not merged. Reconcile them by hand — move this store aside and clone the hub, "+
				"or push this one over the hub if it is the copy you want to keep. (git: %v)", s.remote, err)
		return nil
	}
	report.AdoptedHistory = true
	return nil
}

func (s *Store) writeTemplates(report *Report) error {
	gitignore := filepath.Join(s.Dir, ".gitignore")
	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		if err := writeFile(gitignore, ".DS_Store\n"); err != nil {
			return err
		}
		report.WroteTemplates = append(report.WroteTemplates, ".gitignore")
	}

	schema := filepath.Join(memory.SharedDir(s.Dir), "SCHEMA.md")
	if _, err := os.Stat(schema); os.IsNotExist(err) {
		if err := writeFile(schema, templates.Schema); err != nil {
			return err
		}
		report.WroteTemplates = append(report.WroteTemplates, "shared/SCHEMA.md")
	}
	return nil
}
