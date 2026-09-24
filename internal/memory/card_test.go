package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cardCases are the renders frozen in testdata/cards. Each one says which rules
// of docs/specs/card.md it exercises.
//
// The expected files are written by hand from that document. A render is never
// copied into them: a test whose expectation comes from the code it tests only
// proves the code agrees with itself.
var cardCases = []struct {
	slug    string
	lang    Lang
	machine string
	covers  string
}{
	{"busy-api", EN, "fixture-box",
		"more tasks than the card shows, tasks from the project's own sections, tasks written inside notes"},
	{"busy-api", ES, "other-box",
		"the same project in Spanish, seen from a machine the stamps do not name"},
	{"mixed-languages", EN, "fixture-box",
		"both language variants of the core sections, English first, then a section of its own"},
	{"odd-frontmatter", EN, "other-box",
		"no name, a status outside the known set, and nothing open"},
	{"finished-app", ES, "fixture-box",
		"no pending file and no memories"},
	{"two-machines", EN, "fixture-box",
		"tasks bound to two machines, which the header counts instead of naming"},
	{"two-items", EN, "fixture-box",
		"every task fits, so there is no line counting the rest"},
	{"thin-notes", EN, "fixture-box",
		"memories that are not tasks, so the card has nothing to list"},
	{"paused-web", ES, "other-box",
		"a pending list written entirely in Spanish, with accented section labels as origins"},
}

func expectedCard(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "cards", name+".txt"))
	if err != nil {
		t.Fatalf("reading the expected card: %v", err)
	}
	// The files end with a newline, as text files do; the render does not.
	return strings.TrimSuffix(string(raw), "\n")
}

func TestCard(t *testing.T) {
	for _, c := range cardCases {
		name := c.slug + "." + string(c.lang) + "." + c.machine
		t.Run(name, func(t *testing.T) {
			got, err := RenderCard(fixtureStore, c.slug, CardOptions{Lang: c.lang, Machine: c.machine})
			if err != nil {
				t.Fatalf("rendering: %v", err)
			}
			if want := expectedCard(t, name); got != want {
				t.Errorf("the card changed. This case covers: %s\n\n--- got ---\n%s\n\n--- want ---\n%s", c.covers, got, want)
			}
		})
	}
}

// TestCardCoversEveryProject guards the fixtures themselves: one that no case
// renders is one nobody is checking.
func TestCardCoversEveryProject(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range cardCases {
		covered[c.slug] = true
	}
	for _, slug := range ProjectSlugs(fixtureStore) {
		if !covered[slug] {
			t.Errorf("the fixture project %q has no card case", slug)
		}
	}
}

func TestCardDefaultsToEnglish(t *testing.T) {
	got, err := RenderCard(fixtureStore, "two-items", CardOptions{Machine: "fixture-box"})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if want := expectedCard(t, "two-items.en.fixture-box"); got != want {
		t.Errorf("an unset language must render in English\n--- got ---\n%s", got)
	}
}

func TestCardMissingProject(t *testing.T) {
	store := fixtureStore
	_, err := RenderCard(store, "no-such-project", CardOptions{Lang: EN, Machine: "fixture-box"})
	var notFound *ProjectNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error is %v, want a ProjectNotFoundError", err)
	}
	if notFound.Slug != "no-such-project" || notFound.Store != store {
		t.Errorf("the error names %q in %q, want the slug and store that were asked for", notFound.Slug, notFound.Store)
	}
	if want := "project 'no-such-project' not found in " + store; err.Error() != want {
		t.Errorf("message is %q, want %q", err.Error(), want)
	}
}

func TestResolveLang(t *testing.T) {
	cases := map[string]Lang{
		"en":      EN,
		"es":      ES,
		"ES":      ES,
		"  es  ":  ES,
		"es-419":  ES,
		"english": EN,
		"pt":      EN,
		"":        EN,
	}
	for in, want := range cases {
		if got := ResolveLang(in); got != want {
			t.Errorf("ResolveLang(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCardCarriesOnlyTasks states the rule the layout exists for: a memory that
// is not a task never reaches the card, whatever its type.
func TestCardCarriesOnlyTasks(t *testing.T) {
	got, err := RenderCard(fixtureStore, "busy-api", CardOptions{Lang: EN, Machine: "fixture-box"})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	for _, m := range MemoriesForProject(LoadMemories(fixtureStore), "busy-api") {
		if m.Type == "todo" || m.Summary == "" {
			continue
		}
		if strings.Contains(got, m.Summary) {
			t.Errorf("the card shows %q, a %s: only open tasks belong on it", m.Summary, m.Type)
		}
	}
}
