package memory

import (
	"os"
	"sort"
	"strings"
)

// Project is a project's INDEX.md and pending.md, read together.
type Project struct {
	Slug     string
	Name     string
	Status   string
	Services []string
	Updated  string
	// Description is INDEX.md's body, trimmed.
	Description string
	IndexRaw    string
	Fields      Fields
	Pending     []Section
	// HasPending is false when there is no pending.md, which is a valid state.
	HasPending bool
}

// ProjectSlugs lists the directories under projects/, in code-point order,
// whether or not they hold an INDEX.md. Error messages use this list, so a
// directory that is not yet a project is still worth naming.
func ProjectSlugs(store string) []string {
	entries, err := os.ReadDir(ProjectsDir(store))
	if err != nil {
		return nil
	}
	var slugs []string
	for _, e := range entries {
		if e.IsDir() {
			slugs = append(slugs, e.Name())
		}
	}
	sort.Strings(slugs)
	return slugs
}

// ProjectExists reports whether a slug names a project, which is the same test
// the card and the writes use: an INDEX.md is there.
func ProjectExists(store, slug string) bool {
	_, err := os.Stat(IndexPath(store, slug))
	return err == nil
}

// ReadProject reads a project, and reports false when it has no INDEX.md.
func ReadProject(store, slug string) (Project, bool) {
	indexRaw, err := os.ReadFile(IndexPath(store, slug))
	if err != nil {
		return Project{}, false
	}
	doc := ParseDoc(string(indexRaw))
	f := doc.Fields

	str := func(key, fallback string) string {
		if v := f.Str(key); v != "" {
			return v
		}
		return fallback
	}

	pendingRaw, err := os.ReadFile(PendingPath(store, slug))
	hasPending := err == nil

	return Project{
		Slug:        slug,
		Name:        str("name", slug),
		Status:      str("status", "?"),
		Services:    f.List("services"),
		Updated:     str("updated", ""),
		Description: strings.TrimSpace(doc.Body),
		IndexRaw:    string(indexRaw),
		Fields:      f,
		Pending:     ParsePending(string(pendingRaw)),
		HasPending:  hasPending,
	}, true
}
