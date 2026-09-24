package memory

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// TypeOrder is the render and sort order of memory types, from the data contract.
var TypeOrder = []string{"decision", "constraint", "gotcha", "bug", "reference", "todo"}

// Memory is one atomic fact: one file, tagged with one or more projects.
type Memory struct {
	// ID is the file name without .md, which the data contract makes authoritative.
	ID   string
	Path string
	// DeclaredID is the id written in the frontmatter, which should equal ID.
	DeclaredID string
	Type       string
	Projects   []string
	Services   []string
	Tags       []string
	Updated    string
	Author     string
	// Summary is the first content line of the body, truncated: what the card shows.
	Summary string
	Body    string
	Raw     string
	Fields  Fields
}

// mdMarkers is the run of markdown markers and whitespace a summary line loses.
// RE2's \s is ASCII only, so the Unicode spaces (\p{Z}) and the few C0 controls
// that count as whitespace elsewhere are listed by hand.
var mdMarkers = regexp.MustCompile(`^[#\-*>\s\p{Z}\x{0b}\x{1c}-\x{1f}\x{85}]+`)

// SummaryFromBody returns the first non-empty body line without its leading
// markdown markers, truncated. A body with no content has no summary.
func SummaryFromBody(body string) string {
	for _, line := range SplitLines(body) {
		s := strings.TrimSpace(mdMarkers.ReplaceAllString(strings.TrimSpace(line), ""))
		if s != "" {
			return Truncate(s, MaxItemLen)
		}
	}
	return ""
}

// ParseMemory reads one memory file's content.
func ParseMemory(raw, file string) Memory {
	doc := ParseDoc(raw)
	f := doc.Fields
	return Memory{
		ID:         strings.TrimSuffix(filepath.Base(file), ".md"),
		Path:       file,
		DeclaredID: f.Str("id"),
		Type:       f.Str("type"),
		Projects:   f.List("projects"),
		Services:   f.List("services"),
		Tags:       f.List("tags"),
		Updated:    f.Str("updated"),
		Author:     f.Str("author"),
		Summary:    SummaryFromBody(doc.Body),
		Body:       doc.Body,
		Raw:        raw,
		Fields:     f,
	}
}

// MemoryFiles lists the .md files of a store's memories directory, in code-point
// order. Dotfiles are included: a memory is a memory whatever its name starts
// with, and the card counts it.
func MemoryFiles(store string) []string {
	dir := MemoriesDir(store)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	files := make([]string, 0, len(names))
	for _, name := range names {
		files = append(files, filepath.Join(dir, name))
	}
	return files
}

// LoadMemories reads every memory of a store. A file that cannot be read is
// skipped rather than failing the whole read.
func LoadMemories(store string) []Memory {
	var out []Memory
	for _, file := range MemoryFiles(store) {
		raw, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		out = append(out, ParseMemory(string(raw), file))
	}
	return out
}

// TypeRank orders a type for rendering. An unknown type sorts after all of them.
func TypeRank(t string) int {
	if i := slices.Index(TypeOrder, t); i != -1 {
		return i
	}
	return len(TypeOrder)
}

// MemoriesForProject returns the memories tagged with a slug, ordered by type and
// then, because the sort is stable, by file name.
//
// The filter reads the parsed projects field, never a text search of the slug: a
// short slug matches inside a longer one, and it also matches prose in a body.
func MemoriesForProject(memories []Memory, slug string) []Memory {
	var tagged []Memory
	for _, m := range memories {
		if slices.Contains(m.Projects, slug) {
			tagged = append(tagged, m)
		}
	}
	sort.SliceStable(tagged, func(i, j int) bool {
		return TypeRank(tagged[i].Type) < TypeRank(tagged[j].Type)
	})
	return tagged
}
