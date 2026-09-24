package memory

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// store_test.go has the everyday summaries. These are the edges.
func TestSummaryEdges(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"nested markers and tabs", "> \t# > quoted", "quoted"},
		{"a line of only markers is not content", "***\n---\n#\nreal", "real"},
		{"a rule then text on the same line", "---rule", "rule"},
		{"markers inside the line stay", "keep - this * and # that", "keep - this * and # that"},
		{"a numbered item is not a marker", "1. numbered", "1. numbered"},
		{"unicode spaces between markers are stripped", "- - text", "text"},
		{"an em space is whitespace", "* item", "item"},
		{"an ideographic space is whitespace", "　# item", "item"},
		{"trailing markers stay", "* item *", "item *"},
		{"empty body", "", ""},
		{"only markers", "# \n- \n>", ""},
		{"CRLF body", "\r\n- first\r\nsecond\r\n", "first"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SummaryFromBody(c.body); got != c.want {
				t.Errorf("SummaryFromBody(%q) = %q, want %q", c.body, got, c.want)
			}
		})
	}

	t.Run("truncates to MaxItemLen code points", func(t *testing.T) {
		got := SummaryFromBody("- " + strings.Repeat("🐛", 150))
		if utf8.RuneCountInString(got) != MaxItemLen || !strings.HasSuffix(got, "…") {
			t.Errorf("got %d code points ending in %q, want %d ending in …", utf8.RuneCountInString(got), got[len(got)-3:], MaxItemLen)
		}
		if got != strings.Repeat("🐛", MaxItemLen-1)+"…" {
			t.Errorf("cut mid-character or at the wrong place: %q", got)
		}
	})
}

func TestParseMemory(t *testing.T) {
	raw := "---\nid: declared\nprojects: [a, b]\ntype: gotcha\nservices: api\ntags: []\nauthor: Me\nupdated: 2026-01-02\n---\n- Summary line\nbody\n"
	m := ParseMemory(raw, filepath.Join("store", "memories", "from-file.md"))

	if m.ID != "from-file" || m.DeclaredID != "declared" {
		t.Errorf("ID = %q, DeclaredID = %q: the file name must win", m.ID, m.DeclaredID)
	}
	if m.Type != "gotcha" || m.Author != "Me" || m.Updated != "2026-01-02" {
		t.Errorf("scalars: %+v", m)
	}
	if strings.Join(m.Projects, ",") != "a,b" || strings.Join(m.Services, ",") != "api" || len(m.Tags) != 0 {
		t.Errorf("lists: projects=%q services=%q tags=%q", m.Projects, m.Services, m.Tags)
	}
	if m.Summary != "Summary line" || m.Body != "\n- Summary line\nbody\n" || m.Raw != raw {
		t.Errorf("summary=%q body=%q", m.Summary, m.Body)
	}

	t.Run("a list where a scalar is expected reads as empty", func(t *testing.T) {
		m := ParseMemory("---\ntype: [a, b]\nprojects: solo\n---\n", "x.md")
		if m.Type != "" {
			t.Errorf("Type = %q, want empty", m.Type)
		}
		if strings.Join(m.Projects, ",") != "solo" {
			t.Errorf("Projects = %q, want [solo]", m.Projects)
		}
	})

	t.Run("no frontmatter is a memory with only a body", func(t *testing.T) {
		m := ParseMemory("just text", "x.md")
		if m.Type != "" || m.Projects != nil || m.Summary != "just text" || m.Body != "just text" {
			t.Errorf("%+v", m)
		}
	})
}

func TestMemoryFiles(t *testing.T) {
	store := t.TempDir()
	dir := MemoriesDir(store)
	if err := os.MkdirAll(filepath.Join(dir, "a-directory.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.md", "a.md", ".dot.md", "notes.txt", "Z.md", "é.md", "z.MD", "README"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("---\nid: x\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var got []string
	for _, f := range MemoryFiles(store) {
		if filepath.Dir(f) != dir {
			t.Errorf("%s is not under the memories directory", f)
		}
		got = append(got, filepath.Base(f))
	}
	want := []string{".dot.md", "Z.md", "a.md", "b.md", "é.md"}
	if !slices.Equal(got, want) {
		t.Errorf("MemoryFiles = %q, want %q (dotfiles in, directories and other extensions out, code-point order)", got, want)
	}

	if got := MemoryFiles(filepath.Join(store, "missing")); got != nil {
		t.Errorf("a store with no memories directory gives %q, want nil", got)
	}
}

func TestLoadMemories(t *testing.T) {
	t.Run("fixture store", func(t *testing.T) {
		memories := LoadMemories(fixtureStore)
		files := MemoryFiles(fixtureStore)
		if len(memories) == 0 || len(memories) != len(files) {
			t.Fatalf("loaded %d memories from %d files", len(memories), len(files))
		}
		ids := make([]string, len(memories))
		for i, m := range memories {
			ids[i] = m.ID
			if m.Path != files[i] || m.Raw == "" || m.Fields == nil {
				t.Errorf("%s: incomplete memory: %+v", m.ID, m)
			}
		}
		if !slices.IsSorted(ids) {
			t.Errorf("not in file order: %q", ids)
		}
		if !slices.Contains(ids, ".editor-backup") {
			t.Errorf("the dotfile memory is missing from %q", ids)
		}
	})

	t.Run("a file that cannot be read is skipped", func(t *testing.T) {
		store := t.TempDir()
		dir := MemoriesDir(store)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "good.md"), []byte("---\nid: good\n---\nok"), 0o644); err != nil {
			t.Fatal(err)
		}
		// A dangling symlink is listed by ReadDir but cannot be opened.
		if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "broken.md")); err != nil {
			t.Fatal(err)
		}
		got := LoadMemories(store)
		if len(got) != 1 || got[0].ID != "good" {
			t.Errorf("LoadMemories = %+v, want only good", got)
		}
	})
}

func TestTypeRank(t *testing.T) {
	for i, typ := range TypeOrder {
		if TypeRank(typ) != i {
			t.Errorf("TypeRank(%q) = %d, want %d", typ, TypeRank(typ), i)
		}
	}
	for _, typ := range []string{"", "weird", "Decision", " decision"} {
		if TypeRank(typ) != len(TypeOrder) {
			t.Errorf("TypeRank(%q) = %d, want %d (after every known type)", typ, TypeRank(typ), len(TypeOrder))
		}
	}
}

// store_test.go proves the decoys are not tagged. This pins the whole order.
func TestMemoriesForProjectOrder(t *testing.T) {
	all := LoadMemories(fixtureStore)
	ids := func(ms []Memory) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = m.ID
		}
		return out
	}

	cases := []struct {
		slug string
		want []string
	}{
		{
			// By type order, then file order within a type. The dotfile sorts
			// first among the gotchas because '.' precedes 'o'. The untyped note
			// is last.
			slug: "busy-api",
			want: []string{
				"busy-aaa-first-decision", "busy-auth-jwt-rotation", "busy-md-markers",
				"busy-rate-limit-invariant",
				".editor-backup", "busy-long-summary", "shared-timezone-trap",
				"busy-null-cursor-bug",
				"busy-ref-runbook",
				"busy-empty-body", "busy-todo-metrics",
				"busy-untyped-note",
			},
		},
		{
			slug: "paused-web",
			want: []string{"paused-scalar-project", "shared-timezone-trap", "paused-design-tokens"},
		},
		{slug: "busy", want: nil},
		{slug: "api", want: nil},
		{slug: "no-such-project", want: nil},
		{slug: "", want: nil},
	}
	for _, c := range cases {
		t.Run(c.slug, func(t *testing.T) {
			got := ids(MemoriesForProject(all, c.slug))
			if !slices.Equal(got, c.want) {
				t.Errorf("MemoriesForProject(%q):\n got: %q\nwant: %q", c.slug, got, c.want)
			}
		})
	}

	t.Run("the input order is kept within a type, and the input is not reordered", func(t *testing.T) {
		in := []Memory{
			{ID: "3", Type: "todo", Projects: []string{"p"}},
			{ID: "1", Type: "decision", Projects: []string{"p"}},
			{ID: "4", Type: "todo", Projects: []string{"p"}},
			{ID: "2", Type: "decision", Projects: []string{"p"}},
			{ID: "5", Type: "unknown", Projects: []string{"p"}},
			{ID: "6", Projects: []string{"p"}},
		}
		if got := ids(MemoriesForProject(in, "p")); !slices.Equal(got, []string{"1", "2", "3", "4", "5", "6"}) {
			t.Errorf("got %q", got)
		}
		if in[0].ID != "3" {
			t.Error("the input slice was reordered")
		}
	})
}
