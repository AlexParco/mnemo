package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/AlexParco/mnemo/internal/memory"
)

// Retiring a store: the one destructive operation no agent can reach.
//
// Forgetting a project is behind a confirmation an agent has to show a person.
// Removing everything is a different category, it has no undo, and no prompt
// should be able to get to it. It is driven from the terminal, and what the user
// types is in docs/cli.md.

// ProjectCount is one project and how many memories are tagged with it.
type ProjectCount struct {
	Slug     string `json:"slug"`
	Memories int    `json:"memories"`
}

// RetirePlan is everything that would go.
type RetirePlan struct {
	Store  string `json:"store"`
	Exists bool   `json:"exists"`
	// Projects is every project with its own count, so the person recognises what
	// they are about to lose by name rather than by number.
	Projects    []ProjectCount `json:"projects"`
	Memories    int            `json:"memories"`
	SharedFiles int            `json:"sharedFiles"`
	// Remote is the hub, which is the copy that survives this.
	Remote        string `json:"remote"`
	Unpushed      int    `json:"unpushed"`
	UnpushedKnown bool   `json:"unpushedKnown"`
	// Dirty is work that is not even committed, and exists nowhere else at all.
	Dirty   bool   `json:"dirty"`
	Confirm string `json:"-"`
}

// AtRisk reports whether anything here exists only in this copy.
func (p RetirePlan) AtRisk() bool {
	return p.Dirty || p.Remote == "" && p.Memories > 0 || p.UnpushedKnown && p.Unpushed > 0
}

// RetireOptions change what applying does.
type RetireOptions struct {
	// Purge deletes the store instead of moving it aside.
	Purge bool
	// Force goes ahead even though work has not reached the hub.
	Force bool
}

// RetireResult is what was done.
type RetireResult struct {
	Store string
	// MovedTo is where the store went, empty when it was purged.
	MovedTo string
	Purged  bool
	// Remote is the hub that still has whatever was pushed to it.
	Remote string
}

// PlanRetire reports what retiring this store would take with it.
func (s *Store) PlanRetire(ctx context.Context) (RetirePlan, error) {
	var plan RetirePlan
	err := s.hold(ctx, func() error {
		var err error
		plan, err = s.buildRetirePlan()
		return err
	})
	return plan, err
}

func (s *Store) buildRetirePlan() (RetirePlan, error) {
	plan := RetirePlan{Store: s.Dir}
	if _, err := os.Stat(s.Dir); err != nil {
		return RetirePlan{}, refuse("There is no store at %s, so there is nothing to retire.", s.Dir)
	}
	plan.Exists = true

	memories := memory.LoadMemories(s.Dir)
	plan.Memories = len(memories)
	plan.SharedFiles = len(memory.SharedFiles(s.Dir))
	for _, slug := range memory.ProjectSlugs(s.Dir) {
		if !memory.ProjectExists(s.Dir, slug) {
			continue
		}
		count := 0
		for _, m := range memories {
			if slices.Contains(m.Projects, slug) {
				count++
			}
		}
		plan.Projects = append(plan.Projects, ProjectCount{Slug: slug, Memories: count})
	}

	status := s.repo.Status()
	plan.Remote, plan.Dirty = status.Remote, status.Dirty
	plan.Unpushed, plan.UnpushedKnown = status.Unpushed, status.UnpushedKnown

	confirm, err := confirmFor(plan)
	if err != nil {
		return RetirePlan{}, err
	}
	plan.Confirm = confirm
	return plan, nil
}

// ApplyRetire moves the store aside, or deletes it.
func (s *Store) ApplyRetire(ctx context.Context, confirm string, opts RetireOptions) (RetireResult, error) {
	var result RetireResult
	// The lock file's name comes from the store's resolved path, so it has to be
	// worked out while the store is still there. Afterwards the path no longer
	// resolves and the name would come out different.
	lockFile := s.locker.File(s.Dir)
	err := s.hold(ctx, func() error {
		plan, err := s.buildRetirePlan()
		if err != nil {
			return err
		}
		if err := requireFresh(plan.Confirm, confirm, "retired"); err != nil {
			return err
		}
		if err := requireSafeToRetire(plan, opts.Force); err != nil {
			return err
		}

		result = RetireResult{Store: s.Dir, Remote: plan.Remote, Purged: opts.Purge}
		if opts.Purge {
			if err := os.RemoveAll(s.Dir); err != nil {
				return fmt.Errorf("deleting the store: %w", err)
			}
			return nil
		}

		// The new name sits next to the old one, so the rename stays on one
		// filesystem and cannot half-copy a store.
		target := retiredName(s.Dir, s.Today())
		if err := os.Rename(s.Dir, target); err != nil {
			return fmt.Errorf("moving the store aside: %w", err)
		}
		result.MovedTo = target
		return nil
	})
	if err != nil {
		return RetireResult{}, err
	}

	// The lock file is the one thing outside the store that belongs to it. The
	// mailbox and the config belong to the machine and stay where they are.
	os.Remove(lockFile)
	return result, nil
}

// requireSafeToRetire refuses while this copy is the only one.
func requireSafeToRetire(plan RetirePlan, force bool) error {
	if force || !plan.AtRisk() {
		return nil
	}
	switch {
	case plan.Dirty:
		return refuse("The store has changes that are not even committed, so they exist nowhere else. "+
			"Commit and push them first, or retire it anyway with --force. Nothing was done. (%s)", plan.Store)
	case plan.Remote == "":
		return refuse("This store has no hub, so these %d memories exist only here and retiring it would be the end "+
			"of them. Set a hub and push first, or retire it anyway with --force. Nothing was done.", plan.Memories)
	default:
		return refuse("%d commit(s) have not reached the hub, so that work exists only here. Push first, or retire "+
			"it anyway with --force. Nothing was done.", plan.Unpushed)
	}
}

// retiredName is where a retired store goes, with a counter when a store was
// already retired today.
func retiredName(dir, day string) string {
	base := dir + ".retired-" + day
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return base
	}
	for n := 2; ; n++ {
		candidate := base + "." + strconv.Itoa(n)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// RetiredStores lists what previous retirements left next to this store, so the
// command can print the one line that deletes them for good.
func (s *Store) RetiredStores() []string {
	parent := filepath.Dir(s.Dir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	prefix := filepath.Base(s.Dir) + ".retired-"
	var out []string
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) > len(prefix) && e.Name()[:len(prefix)] == prefix {
			out = append(out, filepath.Join(parent, e.Name()))
		}
	}
	return out
}
