package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/AlexParco/mnemo/internal/memory"
)

// Rename and forget: the two operations that can lose memory, and the only ones
// that work in two calls.
//
// The first call returns a plan and changes nothing. The second carries the
// plan's confirmation, recomputes the plan, and acts only if the two still
// match. The confirmation is a digest of the plan, so it needs no state on this
// side: it survives a restart, it cannot be replayed against different content,
// and applying a plan changes the plan, which makes it single-use by
// construction.
//
// What the code cannot enforce is that a person saw the plan and said yes. That
// lives in the tool descriptions, where every agent reads it.

// confirmFor is the value that unlocks exactly this plan.
func confirmFor(plan any) (string, error) {
	// Confirm fields carry `json:"-"`, so the digest is of everything else.
	canonical, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("hashing the plan: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])[:12], nil
}

func requireFresh(expected, given, what string) error {
	if expected == given {
		return nil
	}
	return refuse("That confirmation does not match the current state of the store — something changed since the "+
		"plan was made, or it was not the value issued. Nothing was %s. Run the plan again, show the user what it "+
		"now says, and confirm from there.", what)
}

// requireCleanTree refuses to fold someone else's uncommitted work into the
// commit this operation is about to make. Git is the only undo there is, so its
// history has to stay readable.
func (s *Store) requireCleanTree() error {
	if s.repo.Status().Dirty {
		return refuse("The store has uncommitted changes. Commit them first with mnemo_commit: this operation makes " +
			"its own commit, and would otherwise fold unrelated work into it.")
	}
	return nil
}

func (s *Store) unknownProject(slug string) error {
	existing := memory.ProjectSlugs(s.Dir)
	if len(existing) > 0 {
		return refuse("No project '%s' in the store. Existing: %s.", slug, strings.Join(existing, ", "))
	}
	return refuse("No project '%s' in the store.", slug)
}

// RenamePlan is what renaming a project's slug would change.
type RenamePlan struct {
	Kind string `json:"kind"`
	From string `json:"from"`
	To   string `json:"to"`
	// Memories are the ids whose `projects` field would be rewritten.
	Memories []string `json:"memories"`
	Confirm  string   `json:"-"`
}

// RenameResult is what renaming did.
type RenameResult struct {
	From            string
	To              string
	MemoriesUpdated []string
	Commit          CommitResult
}

// PlanRename reports what renaming a slug would touch, and changes nothing.
func (s *Store) PlanRename(ctx context.Context, from, to string) (RenamePlan, error) {
	var plan RenamePlan
	err := s.hold(ctx, func() error {
		var err error
		plan, err = s.buildRenamePlan(from, to)
		return err
	})
	return plan, err
}

func (s *Store) buildRenamePlan(from, to string) (RenamePlan, error) {
	if !IsKebab(to) {
		return RenamePlan{}, refuse("'%s' is not a valid slug: use lower-case kebab-case.", to)
	}
	if from == to {
		return RenamePlan{}, refuse("The old and new slugs are the same.")
	}
	if !IsKebab(from) || !memory.ProjectExists(s.Dir, from) {
		return RenamePlan{}, s.unknownProject(from)
	}
	if _, err := os.Stat(memory.ProjectDir(s.Dir, to)); err == nil {
		return RenamePlan{}, refuse("'%s' already exists. Renaming onto it would merge two projects, which this "+
			"operation does not do.", to)
	}

	plan := RenamePlan{Kind: "rename", From: from, To: to}
	for _, m := range memory.LoadMemories(s.Dir) {
		if slices.Contains(m.Projects, from) {
			plan.Memories = append(plan.Memories, m.ID)
		}
	}
	confirm, err := confirmFor(plan)
	if err != nil {
		return RenamePlan{}, err
	}
	plan.Confirm = confirm
	return plan, nil
}

// ApplyRename changes a project's identity across the whole store.
func (s *Store) ApplyRename(ctx context.Context, from, to, confirm string) (RenameResult, error) {
	var result RenameResult
	err := s.hold(ctx, func() error {
		plan, err := s.buildRenamePlan(from, to)
		if err != nil {
			return err
		}
		if err := requireFresh(plan.Confirm, confirm, "renamed"); err != nil {
			return err
		}
		if _, err := s.ensure(); err != nil {
			return err
		}
		if err := s.requireCleanTree(); err != nil {
			return err
		}

		updated := s.Today()
		if err := os.Rename(memory.ProjectDir(s.Dir, from), memory.ProjectDir(s.Dir, to)); err != nil {
			return fmt.Errorf("moving the project: %w", err)
		}

		index := memory.IndexPath(s.Dir, to)
		raw, err := os.ReadFile(index)
		if err != nil {
			return fmt.Errorf("reading the renamed project: %w", err)
		}
		doc := memory.ParseDoc(string(raw)).SetField("slug", memory.Scalar(to)).SetField("updated", memory.Scalar(updated))
		if err := writeFile(index, doc.Render()); err != nil {
			return err
		}

		for _, id := range plan.Memories {
			path := memory.MemoryPath(s.Dir, id)
			raw, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("reading %s: %w", id, err)
			}
			// Only the projects field moves: overlap and order survive, and prose
			// in the body that happens to mention the old slug is left alone.
			doc := memory.ParseDoc(string(raw))
			projects := doc.Fields.List("projects")
			renamed := make([]string, len(projects))
			for i, p := range projects {
				if p == from {
					p = to
				}
				renamed[i] = p
			}
			doc = doc.SetField("projects", memory.ListValue(renamed)).SetField("updated", memory.Scalar(updated))
			if err := writeFile(path, doc.Render()); err != nil {
				return err
			}
		}

		if err := s.verifyGone(from); err != nil {
			return err
		}
		if !memory.ProjectExists(s.Dir, to) {
			return fmt.Errorf("rename left no project at '%s'; this is a bug", to)
		}

		result.From, result.To, result.MemoriesUpdated = from, to, plan.Memories
		result.Commit, err = s.commit(fmt.Sprintf("rename(%s → %s): %d memories retagged", from, to, len(plan.Memories)))
		return err
	})
	return result, err
}

// SharedMemory is a memory that survives forgetting a project, with the projects
// it keeps.
type SharedMemory struct {
	ID        string   `json:"id"`
	Remaining []string `json:"remaining"`
}

// ForgetProjectPlan is what deleting a project would remove and what would
// survive it.
type ForgetProjectPlan struct {
	Kind string `json:"kind"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Exclusive memories are tagged with this project alone: they are deleted.
	Exclusive []string `json:"exclusive"`
	// Shared memories are tagged with others too: they are untagged, never deleted.
	Shared []SharedMemory `json:"shared"`
	// BrokenLinks are files that point at a deleted memory with [[id]].
	BrokenLinks []string `json:"brokenLinks"`
	Confirm     string   `json:"-"`
}

// ForgetMemoryPlan is what deleting one memory would remove.
type ForgetMemoryPlan struct {
	Kind        string   `json:"kind"`
	ID          string   `json:"id"`
	Projects    []string `json:"projects"`
	Summary     string   `json:"summary"`
	BrokenLinks []string `json:"brokenLinks"`
	Confirm     string   `json:"-"`
}

// ForgetResult is what forgetting did.
type ForgetResult struct {
	Target      string
	Deleted     []string
	Untagged    []SharedMemory
	BrokenLinks []string
	Commit      CommitResult
}

// PlanForgetProject reports what deleting a project would do, and changes nothing.
func (s *Store) PlanForgetProject(ctx context.Context, slug string) (ForgetProjectPlan, error) {
	var plan ForgetProjectPlan
	err := s.hold(ctx, func() error {
		var err error
		plan, err = s.buildForgetProjectPlan(slug)
		return err
	})
	return plan, err
}

func (s *Store) buildForgetProjectPlan(slug string) (ForgetProjectPlan, error) {
	if !IsKebab(slug) {
		return ForgetProjectPlan{}, s.unknownProject(slug)
	}
	project, ok := memory.ReadProject(s.Dir, slug)
	if !ok {
		return ForgetProjectPlan{}, s.unknownProject(slug)
	}

	plan := ForgetProjectPlan{Kind: "forget-project", Slug: slug, Name: project.Name}
	for _, m := range memory.LoadMemories(s.Dir) {
		if !slices.Contains(m.Projects, slug) {
			continue
		}
		if len(m.Projects) == 1 {
			plan.Exclusive = append(plan.Exclusive, m.ID)
			continue
		}
		var remaining []string
		for _, p := range m.Projects {
			if p != slug {
				remaining = append(remaining, p)
			}
		}
		plan.Shared = append(plan.Shared, SharedMemory{ID: m.ID, Remaining: remaining})
	}
	plan.BrokenLinks = s.incomingLinks(plan.Exclusive, plan.Exclusive, slug)

	confirm, err := confirmFor(plan)
	if err != nil {
		return ForgetProjectPlan{}, err
	}
	plan.Confirm = confirm
	return plan, nil
}

// PlanForgetMemory reports what deleting one memory would do.
func (s *Store) PlanForgetMemory(ctx context.Context, id string) (ForgetMemoryPlan, error) {
	var plan ForgetMemoryPlan
	err := s.hold(ctx, func() error {
		var err error
		plan, err = s.buildForgetMemoryPlan(id)
		return err
	})
	return plan, err
}

func (s *Store) buildForgetMemoryPlan(id string) (ForgetMemoryPlan, error) {
	if !IsKebab(id) {
		return ForgetMemoryPlan{}, refuse("No memory '%s'. Search for it before assuming it is gone.", id)
	}
	var found *memory.Memory
	for _, m := range memory.LoadMemories(s.Dir) {
		if m.ID == id {
			found = &m
			break
		}
	}
	if found == nil {
		return ForgetMemoryPlan{}, refuse("No memory '%s'. Search for it before assuming it is gone.", id)
	}

	plan := ForgetMemoryPlan{
		Kind:        "forget-memory",
		ID:          id,
		Projects:    found.Projects,
		Summary:     found.Summary,
		BrokenLinks: s.incomingLinks([]string{id}, []string{id}, ""),
	}
	confirm, err := confirmFor(plan)
	if err != nil {
		return ForgetMemoryPlan{}, err
	}
	plan.Confirm = confirm
	return plan, nil
}

// ApplyForgetProject deletes a project and the memories that belong to it alone.
func (s *Store) ApplyForgetProject(ctx context.Context, slug, confirm string) (ForgetResult, error) {
	var result ForgetResult
	err := s.hold(ctx, func() error {
		plan, err := s.buildForgetProjectPlan(slug)
		if err != nil {
			return err
		}
		if err := requireFresh(plan.Confirm, confirm, "deleted"); err != nil {
			return err
		}
		if _, err := s.ensure(); err != nil {
			return err
		}
		if err := s.requireCleanTree(); err != nil {
			return err
		}

		updated := s.Today()
		// Untag before deleting: if anything fails halfway, the memories that must
		// survive are the ones already safe.
		for _, shared := range plan.Shared {
			path := memory.MemoryPath(s.Dir, shared.ID)
			raw, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("reading %s: %w", shared.ID, err)
			}
			doc := memory.ParseDoc(string(raw)).
				SetField("projects", memory.ListValue(shared.Remaining)).
				SetField("updated", memory.Scalar(updated))
			if err := writeFile(path, doc.Render()); err != nil {
				return err
			}
		}
		for _, id := range plan.Exclusive {
			if err := os.Remove(memory.MemoryPath(s.Dir, id)); err != nil {
				return fmt.Errorf("deleting %s: %w", id, err)
			}
		}
		if err := os.RemoveAll(memory.ProjectDir(s.Dir, slug)); err != nil {
			return fmt.Errorf("deleting the project: %w", err)
		}

		if err := s.verifyGone(slug); err != nil {
			return err
		}
		for _, shared := range plan.Shared {
			survivor, ok := findMemory(s.Dir, shared.ID)
			if !ok {
				return fmt.Errorf("a shared memory was deleted: '%s'; this is a bug", shared.ID)
			}
			if strings.Join(survivor.Projects, ",") != strings.Join(shared.Remaining, ",") {
				return fmt.Errorf("untagging '%s' left %v, expected %v; this is a bug",
					shared.ID, survivor.Projects, shared.Remaining)
			}
		}

		result = ForgetResult{Target: slug, Deleted: plan.Exclusive, Untagged: plan.Shared, BrokenLinks: plan.BrokenLinks}
		result.Commit, err = s.commit(fmt.Sprintf("forget(project %s): %d deleted, %d untagged",
			slug, len(plan.Exclusive), len(plan.Shared)))
		return err
	})
	return result, err
}

// ApplyForgetMemory deletes one memory.
func (s *Store) ApplyForgetMemory(ctx context.Context, id, confirm string) (ForgetResult, error) {
	var result ForgetResult
	err := s.hold(ctx, func() error {
		plan, err := s.buildForgetMemoryPlan(id)
		if err != nil {
			return err
		}
		if err := requireFresh(plan.Confirm, confirm, "deleted"); err != nil {
			return err
		}
		if _, err := s.ensure(); err != nil {
			return err
		}
		if err := s.requireCleanTree(); err != nil {
			return err
		}

		if err := os.Remove(memory.MemoryPath(s.Dir, id)); err != nil {
			return fmt.Errorf("deleting %s: %w", id, err)
		}
		result = ForgetResult{Target: id, Deleted: []string{id}, BrokenLinks: plan.BrokenLinks}
		result.Commit, err = s.commit("forget(memory " + id + ")")
		return err
	})
	return result, err
}

// incomingLinks lists the files that point at any of these ids with [[id]].
//
// They are reported, never repaired: a link that now goes nowhere is the user's
// to decide about, and silently editing prose to hide the gap would be worse
// than the gap. Files that are being deleted in the same operation are left out,
// because a dangling link inside a deleted file is nobody's problem.
func (s *Store) incomingLinks(ids, excludeMemories []string, excludeProject string) []string {
	if len(ids) == 0 {
		return nil
	}
	var needles []string
	for _, id := range ids {
		needles = append(needles, "[["+id+"]]")
	}
	contains := func(text string) bool {
		for _, needle := range needles {
			if strings.Contains(text, needle) {
				return true
			}
		}
		return false
	}

	hits := map[string]bool{}
	for _, m := range memory.LoadMemories(s.Dir) {
		if slices.Contains(excludeMemories, m.ID) {
			continue
		}
		if contains(m.Raw) {
			hits["memories/"+m.ID+".md"] = true
		}
	}
	for _, slug := range memory.ProjectSlugs(s.Dir) {
		if slug == excludeProject {
			continue
		}
		for name, path := range map[string]string{
			"projects/" + slug + "/INDEX.md":   memory.IndexPath(s.Dir, slug),
			"projects/" + slug + "/pending.md": memory.PendingPath(s.Dir, slug),
		} {
			raw, err := os.ReadFile(path)
			if err == nil && contains(string(raw)) {
				hits[name] = true
			}
		}
	}

	out := make([]string, 0, len(hits))
	for file := range hits {
		out = append(out, file)
	}
	sort.Strings(out)
	return out
}

// verifyGone is the post-condition of rename and forget: nothing still claims the
// slug. It reads the parsed field, never a text search, so a slug mentioned in
// prose does not look like a leftover tag.
func (s *Store) verifyGone(slug string) error {
	var left []string
	for _, m := range memory.LoadMemories(s.Dir) {
		if slices.Contains(m.Projects, slug) {
			left = append(left, m.ID)
		}
	}
	if len(left) > 0 {
		return fmt.Errorf("'%s' is still tagged on: %s; this is a bug", slug, strings.Join(left, ", "))
	}
	if _, err := os.Stat(memory.ProjectDir(s.Dir, slug)); err == nil {
		return fmt.Errorf("projects/%s/ still exists; this is a bug", slug)
	}
	return nil
}

func findMemory(dir, id string) (memory.Memory, bool) {
	for _, m := range memory.LoadMemories(dir) {
		if m.ID == id {
			return m, true
		}
	}
	return memory.Memory{}, false
}
