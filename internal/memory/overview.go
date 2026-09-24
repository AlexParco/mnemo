package memory

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// statusOrder puts active work first. Anything unrecognised sorts last.
var statusOrder = []string{"active", "paused", "done"}

// ProjectSummary is one row of the overview.
type ProjectSummary struct {
	Slug     string
	Name     string
	Status   string
	Services []string
	Updated  string
	// Memories is how many memories are tagged with this project.
	Memories int
	// Shared is how many of those are tagged with more than one project.
	Shared int
}

// Totals counts the whole store.
type Totals struct {
	Projects int
	Memories int
	// SharedFiles counts the files under shared/: conventions, not per-project memories.
	SharedFiles int
	// OrphanMemories counts memories tagged only with projects that do not exist.
	OrphanMemories int
}

// Overview is what a listing of the store shows.
type Overview struct {
	Store    string
	Exists   bool
	Projects []ProjectSummary
	Totals   Totals
}

func statusRank(status string) int {
	if i := slices.Index(statusOrder, status); i != -1 {
		return i
	}
	return len(statusOrder)
}

// SharedFiles lists the entries of shared/, in code-point order.
func SharedFiles(store string) []string {
	entries, err := os.ReadDir(SharedDir(store))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// BuildOverview reads every project and memory of a store and counts them.
func BuildOverview(store string) Overview {
	slugs := ProjectSlugs(store)
	memories := LoadMemories(store)
	known := map[string]bool{}
	for _, slug := range slugs {
		known[slug] = true
	}

	var projects []ProjectSummary
	for _, slug := range slugs {
		project, ok := ReadProject(store, slug)
		if !ok {
			continue // a directory without INDEX.md is not a project
		}
		tagged, shared := 0, 0
		for _, m := range memories {
			if slices.Contains(m.Projects, slug) {
				tagged++
				if len(m.Projects) > 1 {
					shared++
				}
			}
		}
		projects = append(projects, ProjectSummary{
			Slug:     slug,
			Name:     project.Name,
			Status:   project.Status,
			Services: project.Services,
			Updated:  project.Updated,
			Memories: tagged,
			Shared:   shared,
		})
	}

	sort.SliceStable(projects, func(i, j int) bool {
		a, b := projects[i], projects[j]
		if ra, rb := statusRank(a.Status), statusRank(b.Status); ra != rb {
			return ra < rb
		}
		return b.Updated < a.Updated
	})

	orphans := 0
	for _, m := range memories {
		if len(m.Projects) == 0 {
			continue
		}
		hit := false
		for _, p := range m.Projects {
			if known[p] {
				hit = true
				break
			}
		}
		if !hit {
			orphans++
		}
	}

	_, err := os.Stat(store)
	return Overview{
		Store:    store,
		Exists:   err == nil,
		Projects: projects,
		Totals: Totals{
			Projects:       len(projects),
			Memories:       len(memories),
			SharedFiles:    len(SharedFiles(store)),
			OrphanMemories: orphans,
		},
	}
}

// SearchOptions filters a search. An empty field does not filter.
type SearchOptions struct {
	Query   string
	Project string
	Type    string
	// Limit caps the results. Zero means the default of 25.
	Limit int
}

// DefaultSearchLimit is how many memories a search returns when nothing says
// otherwise.
const DefaultSearchLimit = 25

// Search finds memories by free text, project and type.
//
// The query is split into terms and every term must appear somewhere in the id,
// the type, the body, the tags, the services or the projects. A plain substring
// match would miss the most common query there is: ids are kebab-case while
// people type spaces.
func Search(store string, opts SearchOptions) []Memory {
	limit := opts.Limit
	if limit == 0 {
		limit = DefaultSearchLimit
	}
	var terms []string
	for _, t := range strings.Fields(strings.ToLower(opts.Query)) {
		terms = append(terms, t)
	}

	var found []Memory
	for _, m := range LoadMemories(store) {
		if opts.Project != "" && !slices.Contains(m.Projects, opts.Project) {
			continue
		}
		if opts.Type != "" && m.Type != opts.Type {
			continue
		}
		if len(terms) > 0 {
			haystack := strings.ToLower(strings.Join([]string{
				m.ID,
				m.Type,
				m.Body,
				strings.Join(m.Tags, " "),
				strings.Join(m.Services, " "),
				strings.Join(m.Projects, " "),
			}, "\n"))
			all := true
			for _, t := range terms {
				if !strings.Contains(haystack, t) {
					all = false
					break
				}
			}
			if !all {
				continue
			}
		}
		found = append(found, m)
		if len(found) == limit {
			break
		}
	}
	return found
}

// SharedFile is one file of shared/, with its content.
type SharedFile struct {
	Name    string
	Content string
}

// ProjectContext is everything loading a project reads.
type ProjectContext struct {
	Card     string
	Project  Project
	Memories []Memory
	Shared   []SharedFile
	Machine  string
}

// LoadProjectContext reads a project, its memories, the store's conventions and
// the rendered card. It reports false when the slug names no project.
func LoadProjectContext(store, slug string, lang Lang, machine string) (ProjectContext, bool) {
	project, ok := ReadProject(store, slug)
	if !ok {
		return ProjectContext{}, false
	}
	all := LoadMemories(store)

	var shared []SharedFile
	for _, name := range SharedFiles(store) {
		content, err := os.ReadFile(filepath.Join(SharedDir(store), name))
		if err != nil {
			continue
		}
		shared = append(shared, SharedFile{Name: name, Content: string(content)})
	}

	card, err := RenderCard(store, slug, CardOptions{Lang: lang, Machine: machine, Memories: all})
	if err != nil {
		return ProjectContext{}, false
	}
	return ProjectContext{
		Card:     card,
		Project:  project,
		Memories: MemoriesForProject(all, slug),
		Shared:   shared,
		Machine:  machine,
	}, true
}
