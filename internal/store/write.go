package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/AlexParco/mnemo/internal/memory"
)

// asRefusal is errors.As for a Refusal, kept here so store.go needs no import.
func asRefusal(err error, target **Refusal) bool { return errors.As(err, target) }

// MemoryInput is one fact to write.
//
// Services and Tags tell nil from empty: a nil slice leaves the field as it is,
// and an empty one clears it. That is the difference between "I am not saying"
// and "there are none".
type MemoryInput struct {
	ID       string
	Projects []string
	Type     string
	Body     string
	Services []string
	Tags     []string
	Author   string
	// Overwrite is required to replace a memory that exists.
	Overwrite bool
	// Supersedes is the id of a memory this one replaces. That memory is kept
	// and marked, never deleted.
	Supersedes string
}

// MemoryResult is what writing a memory did.
type MemoryResult struct {
	ID      string
	Path    string
	Created bool
	// Related are memories that share vocabulary with a new one: a nudge towards
	// updating what exists instead of adding a near-duplicate.
	Related []string
	// Superseded is the memory this one replaced, when it replaced any.
	Superseded string
}

// WriteMemory stores one fact.
func (s *Store) WriteMemory(ctx context.Context, in MemoryInput) (MemoryResult, error) {
	if !IsKebab(in.ID) {
		return MemoryResult{}, refuse("'%s' is not a valid memory id: use lower-case kebab-case, e.g. 'checkout-customer-immutable'.", in.ID)
	}
	if !slices.Contains(MemoryTypes, in.Type) {
		return MemoryResult{}, refuse("'%s' is not a memory type. Use one of: %s.", in.Type, strings.Join(MemoryTypes, ", "))
	}
	if strings.TrimSpace(in.Body) == "" {
		return MemoryResult{}, refuse("A memory needs a body: one fact, self-explanatory and concise.")
	}
	if in.Supersedes != "" && in.Supersedes == in.ID {
		return MemoryResult{}, refuse("A memory cannot supersede itself.")
	}
	if err := refuseSecrets("memories/"+in.ID+".md", in.Body); err != nil {
		return MemoryResult{}, err
	}

	var result MemoryResult
	err := s.hold(ctx, func() error {
		if _, err := s.ensure(); err != nil {
			return err
		}
		if err := s.requireProjects(in.Projects); err != nil {
			return err
		}

		path := memory.MemoryPath(s.Dir, in.ID)
		existing, err := os.ReadFile(path)
		exists := err == nil
		if exists && !in.Overwrite {
			return refuse("A memory '%s' already exists. Read it with mnemo_read_memory, merge what you want to keep, "+
				"and write it again with overwrite: true. Writing a fresh body would drop whatever it already holds.", in.ID)
		}

		if in.Supersedes != "" {
			if err := s.checkSupersedes(in.ID, in.Supersedes); err != nil {
				return err
			}
		}

		updated := s.Today()
		if exists {
			if err := writeFile(path, s.updatedMemory(string(existing), in, updated)); err != nil {
				return err
			}
		} else {
			if err := writeFile(path, s.newMemory(in, updated)); err != nil {
				return err
			}
		}

		if in.Supersedes != "" {
			if err := s.markSuperseded(in.Supersedes, in.ID, updated); err != nil {
				return err
			}
			result.Superseded = in.Supersedes
		}

		result.ID, result.Path, result.Created = in.ID, path, !exists
		if !exists {
			result.Related = relatedIDs(s.Dir, in.ID)
		}
		return nil
	})
	return result, err
}

// newMemory is the exact shape the data contract gives.
func (s *Store) newMemory(in MemoryInput, updated string) string {
	author := in.Author
	if author == "" {
		author = s.defaultAuthor()
	}
	lines := []string{
		"id: " + in.ID,
		"projects: " + memory.ListValue(in.Projects).Render(),
	}
	if len(in.Services) > 0 {
		lines = append(lines, "services: "+memory.ListValue(in.Services).Render())
	}
	if len(in.Tags) > 0 {
		lines = append(lines, "tags: "+memory.ListValue(in.Tags).Render())
	}
	lines = append(lines, "type: "+in.Type, "author: "+author, "updated: "+updated)
	return "---\n" + strings.Join(lines, "\n") + "\n---\n\n" + strings.TrimSpace(in.Body) + "\n"
}

// updatedMemory rewrites field by field, so unknown keys and the original author
// survive. Rename and forget depend on the same surgery.
func (s *Store) updatedMemory(raw string, in MemoryInput, updated string) string {
	doc := memory.ParseDoc(raw)
	doc = doc.SetField("id", memory.Scalar(in.ID))
	doc = doc.SetField("projects", memory.ListValue(in.Projects))
	if in.Services != nil {
		doc = doc.SetField("services", memory.ListValue(in.Services))
	}
	if in.Tags != nil {
		doc = doc.SetField("tags", memory.ListValue(in.Tags))
	}
	doc = doc.SetField("type", memory.Scalar(in.Type))
	if in.Author != "" {
		doc = doc.SetField("author", memory.Scalar(in.Author))
	}
	doc = doc.SetField("updated", memory.Scalar(updated))
	return doc.WithBody("\n\n" + strings.TrimSpace(in.Body) + "\n").Render()
}

// checkSupersedes refuses a chain that closes on itself. Following it also
// proves the target exists.
func (s *Store) checkSupersedes(newID, target string) error {
	if !IsKebab(target) {
		return refuse("'%s' is not a valid memory id.", target)
	}
	memories := memory.LoadMemories(s.Dir)
	byID := map[string]memory.Memory{}
	for _, m := range memories {
		byID[m.ID] = m
	}
	if _, ok := byID[target]; !ok {
		return refuse("No memory '%s' to supersede. Search for it with mnemo_search_memories; nothing was written.", target)
	}

	// `superseded_by` points forward, from the memory that was replaced to the one
	// that replaced it. Writing this memory adds the edge target → newID, so the
	// loop closes when a path already runs from newID to target.
	seen := map[string]bool{}
	for at := newID; at != "" && !seen[at]; at = byID[at].Fields.Str("superseded_by") {
		seen[at] = true
		if at == target {
			return refuse("'%s' was already replaced by '%s', directly or through another memory, "+
				"so superseding it now would close a loop. Nothing was written.", newID, target)
		}
	}
	return nil
}

// markSuperseded points the old memory at the one that replaced it. The old file
// stays: someone asking why a decision changed needs both, and deleting it would
// leave only the new answer with no trace of the question.
func (s *Store) markSuperseded(target, by, updated string) error {
	path := memory.MemoryPath(s.Dir, target)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading the memory being superseded: %w", err)
	}
	doc := memory.ParseDoc(string(raw))
	doc = doc.SetField("superseded_by", memory.Scalar(by))
	doc = doc.SetField("updated", memory.Scalar(updated))
	return writeFile(path, doc.Render())
}

// relatedIDs finds memories whose id shares words with a new one.
func relatedIDs(dir, id string) []string {
	var terms []string
	for _, term := range strings.Split(id, "-") {
		if len(term) >= 4 {
			terms = append(terms, term)
		}
	}
	if len(terms) == 0 {
		return nil
	}

	type scored struct {
		id    string
		score int
		at    int
	}
	var found []scored
	for i, m := range memory.LoadMemories(dir) {
		if m.ID == id {
			continue
		}
		haystack := strings.ToLower(m.ID + "\n" + m.Body)
		score := 0
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				score++
			}
		}
		if score > 0 {
			found = append(found, scored{m.ID, score, i})
		}
	}
	sort.SliceStable(found, func(a, b int) bool { return found[a].score > found[b].score })

	var out []string
	for _, f := range found[:min(len(found), 5)] {
		out = append(out, f.id)
	}
	return out
}

// PendingResult is what replacing a pending list did.
type PendingResult struct {
	Slug     string
	Path     string
	Sections []PendingSection
}

// PendingSection is one heading with its counts.
type PendingSection struct {
	Label string
	Open  int
	Done  int
}

// WritePending replaces a project's pending list.
//
// It replaces the whole file, so the caller loads the project first and sends the
// merged result. Sections stay free-form: this writes what it is given and only
// reports back what it parsed.
func (s *Store) WritePending(ctx context.Context, slug, content string) (PendingResult, error) {
	if !IsKebab(slug) {
		return PendingResult{}, refuse("'%s' is not a valid project slug.", slug)
	}
	if err := refuseSecrets("projects/"+slug+"/pending.md", content); err != nil {
		return PendingResult{}, err
	}

	var result PendingResult
	err := s.hold(ctx, func() error {
		if _, err := s.ensure(); err != nil {
			return err
		}
		if !memory.ProjectExists(s.Dir, slug) {
			return s.requireProjects([]string{slug})
		}

		body := content
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		path := memory.PendingPath(s.Dir, slug)
		if err := writeFile(path, body); err != nil {
			return err
		}

		result.Slug, result.Path = slug, path
		for _, section := range memory.ParsePending(body) {
			counted := PendingSection{Label: section.Label}
			for _, item := range section.Items {
				if item.Done {
					counted.Done++
				} else {
					counted.Open++
				}
			}
			result.Sections = append(result.Sections, counted)
		}
		return nil
	})
	return result, err
}

// ProjectInput creates or updates a project.
type ProjectInput struct {
	Slug        string
	Name        string
	Status      string
	Services    []string
	Description *string
}

// ProjectResult is what an upsert did.
type ProjectResult struct {
	Slug    string
	Path    string
	Created bool
	Report  Report
}

// ProjectStatuses are the three states a project can be in.
var ProjectStatuses = []string{"active", "paused", "done"}

// UpsertProject creates a project, or updates the fields it is given.
func (s *Store) UpsertProject(ctx context.Context, in ProjectInput) (ProjectResult, error) {
	if !IsKebab(in.Slug) {
		return ProjectResult{}, refuse("'%s' is not a valid project slug: use lower-case kebab-case, e.g. 'orion-api'.", in.Slug)
	}
	if in.Status != "" && !slices.Contains(ProjectStatuses, in.Status) {
		return ProjectResult{}, refuse("'%s' is not a project status. Use one of: %s.", in.Status, strings.Join(ProjectStatuses, ", "))
	}
	if in.Description != nil {
		if err := refuseSecrets("projects/"+in.Slug+"/INDEX.md", *in.Description); err != nil {
			return ProjectResult{}, err
		}
	}

	var result ProjectResult
	err := s.hold(ctx, func() error {
		report, err := s.ensure()
		if err != nil {
			return err
		}
		result.Report = report

		path := memory.IndexPath(s.Dir, in.Slug)
		existing, exists := memory.ReadProject(s.Dir, in.Slug)
		result.Slug, result.Path, result.Created = in.Slug, path, !exists
		updated := s.Today()

		if !exists {
			if in.Name == "" {
				return refuse("Creating project '%s' needs a readable name.", in.Slug)
			}
			status := in.Status
			if status == "" {
				status = "active"
			}
			fm := []string{
				"slug: " + in.Slug,
				"name: " + in.Name,
				"status: " + status,
				"services: " + memory.ListValue(in.Services).Render(),
				"updated: " + updated,
			}
			body := ""
			if in.Description != nil && strings.TrimSpace(*in.Description) != "" {
				body = "\n" + strings.TrimSpace(*in.Description) + "\n"
			}
			if err := os.MkdirAll(memory.ProjectDir(s.Dir, in.Slug), 0o755); err != nil {
				return fmt.Errorf("creating the project: %w", err)
			}
			if err := writeFile(path, "---\n"+strings.Join(fm, "\n")+"\n---\n\n# "+in.Name+"\n"+body); err != nil {
				return err
			}
			return writeFile(memory.PendingPath(s.Dir, in.Slug), "# Pending — "+in.Name+"\n")
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading the project: %w", err)
		}
		doc := memory.ParseDoc(string(raw))
		if in.Name != "" {
			doc = doc.SetField("name", memory.Scalar(in.Name))
		}
		if in.Status != "" {
			doc = doc.SetField("status", memory.Scalar(in.Status))
		}
		if in.Services != nil {
			doc = doc.SetField("services", memory.ListValue(in.Services))
		}
		doc = doc.SetField("updated", memory.Scalar(updated))
		if in.Description != nil {
			name := in.Name
			if name == "" {
				name = existing.Name
			}
			doc = doc.WithBody("\n\n# " + name + "\n\n" + strings.TrimSpace(*in.Description) + "\n")
		}
		return writeFile(path, doc.Render())
	})
	return result, err
}

// CommitResult is what a commit did, or why there was nothing to do.
type CommitResult struct {
	Committed bool
	SHA       string
	Files     []string
	// StrippedTrailers counts the Co-Authored-By lines removed from the message.
	StrippedTrailers int
	Unpushed         int
	UnpushedKnown    bool
	HasRemote        bool
}

// Commit records everything written since the last one, so that one session
// becomes one commit.
func (s *Store) Commit(ctx context.Context, message string) (CommitResult, error) {
	var result CommitResult
	err := s.hold(ctx, func() error {
		var err error
		result, err = s.commit(message)
		return err
	})
	return result, err
}

// commit assumes the lock is held. The operations that make their own commit —
// rename, forget — go through here, because the lock is not reentrant.
func (s *Store) commit(message string) (CommitResult, error) {
	var result CommitResult
	if _, err := s.ensure(); err != nil {
		return result, err
	}

	if _, ok := s.repo.Identity(); !ok {
		return result, refuse("git has no committer identity, so the store cannot be committed. Set one:\n"+
			"  git -C %s config user.name \"Your Name\"\n"+
			"  git -C %s config user.email \"you@example.com\"", s.Dir, s.Dir)
	}

	// The store never carries these: whoever ran the agent is the author, and a
	// trailer naming a model in a memory's history helps nobody.
	var kept []string
	stripped := 0
	for _, line := range strings.Split(message, "\n") {
		if coAuthored.MatchString(line) {
			stripped++
			continue
		}
		kept = append(kept, line)
	}
	clean := strings.TrimSpace(strings.Join(kept, "\n"))
	if clean == "" {
		return result, refuse("A commit needs a message.")
	}
	result.StrippedTrailers = stripped

	if err := s.repo.StageAll(); err != nil {
		return result, err
	}
	staged := s.repo.StagedFiles()
	status := s.repo.Status()
	result.HasRemote, result.Unpushed, result.UnpushedKnown = status.HasRemote(), status.Unpushed, status.UnpushedKnown
	if len(staged) == 0 {
		return result, nil
	}

	sha, err := s.repo.Commit(clean)
	if err != nil {
		return result, err
	}
	status = s.repo.Status()
	result.Committed, result.SHA, result.Files = true, sha, staged
	result.HasRemote, result.Unpushed, result.UnpushedKnown = status.HasRemote(), status.Unpushed, status.UnpushedKnown
	return result, nil
}
