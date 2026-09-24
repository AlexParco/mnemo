package store

import (
	"os"
	"strings"
	"testing"

	"github.com/AlexParco/mnemo/internal/memory"
)

// withDecoys is a store built around the two mistakes a text search makes: a
// slug that appears inside a longer slug, and a slug written in a body. Both
// must survive every destructive operation.
func withDecoys(t *testing.T) *Store {
	t.Helper()
	isolate(t, true)
	s := newStore(t, Options{})

	for _, p := range []struct{ slug, name string }{
		{"orders", "Orders"}, {"orders-v2", "Orders v2"}, {"billing", "Billing"},
	} {
		if _, err := s.UpsertProject(t.Context(), ProjectInput{Slug: p.slug, Name: p.name}); err != nil {
			t.Fatal(err)
		}
	}

	memories := []MemoryInput{
		{ID: "orders-only", Projects: []string{"orders"}, Type: "decision", Body: "Belongs to orders alone."},
		{ID: "orders-and-billing", Projects: []string{"orders", "billing"}, Type: "gotcha", Body: "Shared by two projects."},
		{ID: "decoy-in-prose", Projects: []string{"billing"}, Type: "reference", Body: "The body says projects: [orders] on purpose."},
		{ID: "decoy-longer-slug", Projects: []string{"orders-v2"}, Type: "decision", Body: "Tagged to the longer slug."},
		{ID: "points-at-orders-only", Projects: []string{"billing"}, Type: "note-like", Body: "See [[orders-only]] for the reason."},
	}
	for _, in := range memories {
		if in.Type == "note-like" {
			in.Type = "reference"
		}
		if _, err := s.WriteMemory(t.Context(), in); err != nil {
			t.Fatalf("writing %s: %v", in.ID, err)
		}
	}
	if _, err := s.Commit(t.Context(), "save: the fixture"); err != nil {
		t.Fatal(err)
	}
	return s
}

func projectsOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	m, ok := findMemory(s.Dir, id)
	if !ok {
		t.Fatalf("memory %s is gone", id)
	}
	return strings.Join(m.Projects, ",")
}

func TestRenamePlanNamesWhatWouldChange(t *testing.T) {
	s := withDecoys(t)

	plan, err := s.PlanRename(t.Context(), "orders", "purchases")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.Memories, ",") != "orders-and-billing,orders-only" {
		t.Errorf("memories = %q, want the two tagged with orders, in file order", plan.Memories)
	}
	if plan.Confirm == "" {
		t.Error("the plan has no confirmation to act on")
	}
	if !memory.ProjectExists(s.Dir, "orders") || memory.ProjectExists(s.Dir, "purchases") {
		t.Error("planning changed the store")
	}
}

func TestRenameMovesTheProjectAndRetagsNothingElse(t *testing.T) {
	s := withDecoys(t)
	plan, err := s.PlanRename(t.Context(), "orders", "purchases")
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ApplyRename(t.Context(), "orders", "purchases", plan.Confirm)
	if err != nil {
		t.Fatalf("renaming: %v", err)
	}
	if len(got.MemoriesUpdated) != 2 || !got.Commit.Committed {
		t.Errorf("result = %+v, want two memories and a commit", got)
	}

	if memory.ProjectExists(s.Dir, "orders") || !memory.ProjectExists(s.Dir, "purchases") {
		t.Error("the project did not move")
	}
	if project, _ := memory.ReadProject(s.Dir, "purchases"); project.Fields.Str("slug") != "purchases" {
		t.Error("INDEX.md still carries the old slug")
	}
	if got := projectsOf(t, s, "orders-only"); got != "purchases" {
		t.Errorf("orders-only = %q", got)
	}
	// Overlap and order survive: the memory keeps both projects, renamed in place.
	if got := projectsOf(t, s, "orders-and-billing"); got != "purchases,billing" {
		t.Errorf("the shared memory = %q, want the rename in place", got)
	}
	// The decoys are untouched: one names the slug in prose, the other is a
	// longer slug that contains it.
	if got := projectsOf(t, s, "decoy-longer-slug"); got != "orders-v2" {
		t.Errorf("the longer slug was renamed too: %q", got)
	}
	decoy, _ := findMemory(s.Dir, "decoy-in-prose")
	if !strings.Contains(decoy.Body, "projects: [orders]") {
		t.Error("prose in a body was rewritten")
	}
	// Not even inside a memory that is being retagged: one field changes, the
	// words the user wrote do not.
	retagged, _ := findMemory(s.Dir, "orders-only")
	if !strings.Contains(retagged.Body, "Belongs to orders alone.") {
		t.Errorf("the retagged memory's own prose was rewritten: %q", retagged.Body)
	}

	if message, _ := s.Repo().Try("log", "-1", "--format=%s"); message != "rename(orders → purchases): 2 memories retagged" {
		t.Errorf("the commit says %q", message)
	}
}

func TestRenameRefusals(t *testing.T) {
	s := withDecoys(t)
	cases := []struct{ name, from, to, says string }{
		{"onto a project that exists", "orders", "billing", "merge two projects"},
		{"a slug that is not kebab-case", "orders", "Not Kebab", "kebab-case"},
		{"a project that does not exist", "ghost", "whatever", "No project 'ghost'"},
		{"the same slug", "orders", "orders", "the same"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.PlanRename(t.Context(), c.from, c.to)
			if !IsRefusal(err) {
				t.Fatalf("got %v, want a refusal", err)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the message is %q, want it to mention %q", err, c.says)
			}
		})
	}
}

// A confirmation is bound to the plan it was issued for. Anything that changes
// what would happen has to invalidate it.
func TestAConfirmationGoesStale(t *testing.T) {
	s := withDecoys(t)
	plan, err := s.PlanRename(t.Context(), "orders", "purchases")
	if err != nil {
		t.Fatal(err)
	}

	// Another agent tags one more memory with the project being renamed.
	if _, err := s.WriteMemory(t.Context(), MemoryInput{
		ID: "arrived-late", Projects: []string{"orders"}, Type: "todo", Body: "Written after the plan was made.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(t.Context(), "save: a late arrival"); err != nil {
		t.Fatal(err)
	}

	_, err = s.ApplyRename(t.Context(), "orders", "purchases", plan.Confirm)
	if !IsRefusal(err) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if !memory.ProjectExists(s.Dir, "orders") {
		t.Error("the stale plan was applied anyway")
	}
}

func TestAWrongConfirmationChangesNothing(t *testing.T) {
	s := withDecoys(t)
	if _, err := s.ApplyRename(t.Context(), "orders", "purchases", "000000000000"); !IsRefusal(err) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if !memory.ProjectExists(s.Dir, "orders") {
		t.Error("the project moved anyway")
	}
}

func TestUncommittedWorkIsNotFoldedIn(t *testing.T) {
	s := withDecoys(t)
	plan, err := s.PlanRename(t.Context(), "orders", "purchases")
	if err != nil {
		t.Fatal(err)
	}

	// Someone's work in progress, not committed yet.
	if err := os.WriteFile(memory.MemoryPath(s.Dir, "in-progress"), []byte("---\nid: in-progress\nprojects: [billing]\ntype: todo\n---\n\nHalf written.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = s.ApplyRename(t.Context(), "orders", "purchases", plan.Confirm)
	if !IsRefusal(err) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "mnemo_commit") {
		t.Errorf("the message does not say what to do: %s", err)
	}
	if !memory.ProjectExists(s.Dir, "orders") {
		t.Error("it renamed anyway")
	}
}

func TestForgetProjectPlanSplitsWhatDiesFromWhatSurvives(t *testing.T) {
	s := withDecoys(t)

	plan, err := s.PlanForgetProject(t.Context(), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.Exclusive, ",") != "orders-only" {
		t.Errorf("exclusive = %q, want the memory tagged with orders alone", plan.Exclusive)
	}
	if len(plan.Shared) != 1 || plan.Shared[0].ID != "orders-and-billing" ||
		strings.Join(plan.Shared[0].Remaining, ",") != "billing" {
		t.Errorf("shared = %+v, want the overlapping memory keeping billing", plan.Shared)
	}
	if strings.Join(plan.BrokenLinks, ",") != "memories/points-at-orders-only.md" {
		t.Errorf("broken links = %q, want the file that points at what would go", plan.BrokenLinks)
	}
	if !memory.ProjectExists(s.Dir, "orders") {
		t.Error("planning deleted something")
	}
}

func TestForgetProjectKeepsWhatIsShared(t *testing.T) {
	s := withDecoys(t)
	plan, err := s.PlanForgetProject(t.Context(), "orders")
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ApplyForgetProject(t.Context(), "orders", plan.Confirm)
	if err != nil {
		t.Fatalf("forgetting: %v", err)
	}
	if strings.Join(got.Deleted, ",") != "orders-only" || len(got.Untagged) != 1 {
		t.Errorf("result = %+v", got)
	}

	if memory.ProjectExists(s.Dir, "orders") {
		t.Error("the project is still there")
	}
	if _, ok := findMemory(s.Dir, "orders-only"); ok {
		t.Error("the exclusive memory was not deleted")
	}
	// The overlapping memory is the one that must survive, tagged only with what
	// remains. Deleting it would take another project's knowledge with it.
	if got := projectsOf(t, s, "orders-and-billing"); got != "billing" {
		t.Errorf("the shared memory = %q, want it kept and untagged", got)
	}
	if _, ok := findMemory(s.Dir, "decoy-longer-slug"); !ok {
		t.Error("the longer slug's memory was deleted")
	}
	if _, ok := findMemory(s.Dir, "decoy-in-prose"); !ok {
		t.Error("the memory that only mentions the slug in prose was deleted")
	}

	// A link that now goes nowhere is reported, never repaired.
	pointer, _ := findMemory(s.Dir, "points-at-orders-only")
	if !strings.Contains(pointer.Body, "[[orders-only]]") {
		t.Error("a dangling link was silently edited out")
	}
	if message, _ := s.Repo().Try("log", "-1", "--format=%s"); message != "forget(project orders): 1 deleted, 1 untagged" {
		t.Errorf("the commit says %q", message)
	}
}

// Untagging rewrites one field. Everything else in the file, including keys
// mnemo knows nothing about, stays exactly as it was.
func TestUntaggingIsSurgical(t *testing.T) {
	s := withDecoys(t)
	path := memory.MemoryPath(s.Dir, "orders-and-billing")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	withExtra := strings.Replace(string(raw), "type: gotcha", "type: gotcha\nreviewed-by: a-colleague", 1)
	if err := os.WriteFile(path, []byte(withExtra), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(t.Context(), "save: a key mnemo does not know"); err != nil {
		t.Fatal(err)
	}

	plan, err := s.PlanForgetProject(t.Context(), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyForgetProject(t.Context(), "orders", plan.Confirm); err != nil {
		t.Fatal(err)
	}

	after := read(t, path)
	if !strings.Contains(after, "reviewed-by: a-colleague") {
		t.Errorf("an unknown key was dropped:\n%s", after)
	}
	if !strings.Contains(after, "Shared by two projects.") {
		t.Errorf("the body changed:\n%s", after)
	}
}

func TestForgetMemory(t *testing.T) {
	s := withDecoys(t)

	plan, err := s.PlanForgetMemory(t.Context(), "orders-only")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary == "" || strings.Join(plan.Projects, ",") != "orders" {
		t.Errorf("plan = %+v, want it to describe what would go", plan)
	}
	if strings.Join(plan.BrokenLinks, ",") != "memories/points-at-orders-only.md" {
		t.Errorf("broken links = %q", plan.BrokenLinks)
	}

	got, err := s.ApplyForgetMemory(t.Context(), "orders-only", plan.Confirm)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findMemory(s.Dir, "orders-only"); ok {
		t.Error("the memory is still there")
	}
	if !got.Commit.Committed {
		t.Error("deleting made no commit of its own")
	}
	if message, _ := s.Repo().Try("log", "-1", "--format=%s"); message != "forget(memory orders-only)" {
		t.Errorf("the commit says %q", message)
	}

	if _, err := s.PlanForgetMemory(t.Context(), "never-existed"); !IsRefusal(err) {
		t.Errorf("an unknown id gave %v, want a refusal", err)
	}
}

// Applying twice cannot happen: the second call recomputes the plan, which is
// now a different plan, so the old confirmation no longer matches.
func TestApplyingTwiceIsRefused(t *testing.T) {
	s := withDecoys(t)
	plan, err := s.PlanForgetMemory(t.Context(), "orders-only")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyForgetMemory(t.Context(), "orders-only", plan.Confirm); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyForgetMemory(t.Context(), "orders-only", plan.Confirm); !IsRefusal(err) {
		t.Errorf("the second apply gave %v, want a refusal", err)
	}
}

func TestForgetUnknownProject(t *testing.T) {
	s := withDecoys(t)
	_, err := s.PlanForgetProject(t.Context(), "ghost")
	if !IsRefusal(err) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "billing") {
		t.Errorf("the message does not list the real slugs: %s", err)
	}
}
