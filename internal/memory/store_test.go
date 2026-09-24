package memory

import (
	"slices"
	"strings"
	"testing"
)

// Reading a project, the overview, search and the whole context. Memories
// themselves are covered in memory_test.go.

// fixtureStore is a store written before this code existed. Every test reads it
// and none writes to it, so the package is always exercised against data it did
// not produce.
const fixtureStore = "../../testdata/store"

func TestProjectReads(t *testing.T) {
	store := fixtureStore

	t.Run("directories are the source of truth for slugs", func(t *testing.T) {
		// Code-point order of the directory names, which is what the listing gives.
		want := "busy-api,finished-app,mixed-languages,odd-frontmatter,paused-web,thin-notes,two-items,two-machines"
		if got := strings.Join(ProjectSlugs(store), ","); got != want {
			t.Errorf("slugs = %q, want %q", got, want)
		}
	})

	t.Run("a project with no pending file is still a project", func(t *testing.T) {
		project, ok := ReadProject(store, "finished-app")
		if !ok {
			t.Fatal("finished-app must read as a project")
		}
		if project.HasPending {
			t.Error("finished-app has no pending.md, and that is a valid state")
		}
		if project.Name != "Finished App" || project.Status != "done" {
			t.Errorf("project = %+v, want its frontmatter values", project)
		}
	})

	t.Run("missing values fall back", func(t *testing.T) {
		project, ok := ReadProject(store, "odd-frontmatter")
		if !ok {
			t.Fatal("odd-frontmatter must read as a project")
		}
		if project.Name != "odd-frontmatter" {
			t.Errorf("name = %q, want the slug when the frontmatter name is empty", project.Name)
		}
		if got := strings.Join(project.Services, ","); got != "solo-service" {
			t.Errorf("services = %q, want the scalar read as one item", got)
		}
		if project.Status != "archived" {
			t.Errorf("status = %q, want the value as written, known or not", project.Status)
		}
	})

	t.Run("an unknown slug is not a project", func(t *testing.T) {
		if _, ok := ReadProject(store, "no-such-project"); ok {
			t.Error("an unknown slug must not read as a project")
		}
		if ProjectExists(store, "no-such-project") {
			t.Error("an unknown slug must not exist")
		}
	})
}

func TestOverview(t *testing.T) {
	o := BuildOverview(fixtureStore)

	t.Run("active projects come first, then by date", func(t *testing.T) {
		var order, statuses []string
		for _, p := range o.Projects {
			order = append(order, p.Slug)
			statuses = append(statuses, p.Status)
		}
		wantOrder := "thin-notes,two-machines,busy-api,mixed-languages,two-items,paused-web,finished-app,odd-frontmatter"
		if got := strings.Join(order, ","); got != wantOrder {
			t.Errorf("order = %q, want %q", got, wantOrder)
		}
		wantStatuses := "active,active,active,active,paused,paused,done,archived"
		if got := strings.Join(statuses, ","); got != wantStatuses {
			t.Errorf("statuses = %q, want %q", got, wantStatuses)
		}
	})

	t.Run("counts separate the shared memories", func(t *testing.T) {
		var busy ProjectSummary
		for _, p := range o.Projects {
			if p.Slug == "busy-api" {
				busy = p
			}
		}
		if busy.Memories != 12 {
			t.Errorf("busy-api has %d memories, want 12", busy.Memories)
		}
		if busy.Shared != 1 {
			t.Errorf("busy-api shares %d memories, want only the timezone note", busy.Shared)
		}
	})

	t.Run("totals count the whole store", func(t *testing.T) {
		if o.Totals.Projects != 8 {
			t.Errorf("projects = %d, want 8", o.Totals.Projects)
		}
		if o.Totals.Memories != 17 {
			t.Errorf("memories = %d, want every file in memories/, the dotfile included", o.Totals.Memories)
		}
		if o.Totals.SharedFiles != 2 {
			t.Errorf("shared files = %d, want 2", o.Totals.SharedFiles)
		}
		if o.Totals.OrphanMemories != 1 {
			t.Errorf("orphans = %d, want only the decoy tagged to a project that does not exist", o.Totals.OrphanMemories)
		}
	})
}

func TestSearch(t *testing.T) {
	store := fixtureStore
	ids := func(opts SearchOptions) []string {
		var out []string
		for _, m := range Search(store, opts) {
			out = append(out, m.ID)
		}
		return out
	}

	t.Run("the body matches, whatever the case", func(t *testing.T) {
		if got := ids(SearchOptions{Query: "REFRESH TOKENS"}); strings.Join(got, ",") != "busy-auth-jwt-rotation" {
			t.Errorf("hits = %q, want the JWT note", got)
		}
	})

	t.Run("a spaced query matches a kebab-case id", func(t *testing.T) {
		if got := ids(SearchOptions{Query: "rate limit"}); strings.Join(got, ",") != "busy-rate-limit-invariant" {
			t.Errorf("hits = %q, want the rate limit note", got)
		}
	})

	t.Run("every term must appear", func(t *testing.T) {
		if got := ids(SearchOptions{Query: "cursor unicorn"}); len(got) != 0 {
			t.Errorf("hits = %q, want none", got)
		}
		if got := ids(SearchOptions{Query: "cursor"}); len(got) == 0 {
			t.Error("the same query without the impossible term must find something")
		}
	})

	t.Run("filters compose", func(t *testing.T) {
		if got := ids(SearchOptions{Project: "busy-api", Type: "constraint"}); strings.Join(got, ",") != "busy-rate-limit-invariant" {
			t.Errorf("hits = %q, want the one constraint of busy-api", got)
		}
	})

	t.Run("the limit is honoured", func(t *testing.T) {
		if got := ids(SearchOptions{Project: "busy-api", Limit: 3}); len(got) != 3 {
			t.Errorf("got %d hits, want 3", len(got))
		}
		if got := ids(SearchOptions{Project: "busy-api"}); len(got) != 12 {
			t.Errorf("got %d hits with no limit, want every memory of the project", len(got))
		}
	})
}

func TestLoadProjectContext(t *testing.T) {
	store := fixtureStore
	ctx, ok := LoadProjectContext(store, "busy-api", EN, "fixture-box")
	if !ok {
		t.Fatal("busy-api must load")
	}
	if !strings.HasPrefix(ctx.Card, "busy-api — Busy API") {
		t.Errorf("the context must carry the card, got %q", strings.SplitN(ctx.Card, "\n", 2)[0])
	}
	if len(ctx.Memories) != 12 {
		t.Errorf("got %d memories, want the project's 12", len(ctx.Memories))
	}
	var names []string
	for _, s := range ctx.Shared {
		names = append(names, s.Name)
		if s.Content == "" {
			t.Errorf("%s came back empty", s.Name)
		}
	}
	if !slices.Contains(names, "CONVENTIONS.md") {
		t.Errorf("shared files = %q, want the store's conventions", names)
	}
	if ctx.Machine != "fixture-box" {
		t.Errorf("machine = %q, want the caller's", ctx.Machine)
	}

	if _, ok := LoadProjectContext(store, "no-such-project", EN, "fixture-box"); ok {
		t.Error("an unknown slug must not load")
	}
}
