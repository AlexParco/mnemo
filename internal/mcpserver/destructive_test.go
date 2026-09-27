package mcpserver

import (
	"os"
	"strings"
	"testing"

	"github.com/AlexParco/mnemo/internal/memory"
)

// withOverlap is the store both destructive tools need: a memory that belongs to
// two projects, which is the one that must survive, and a longer slug that
// contains the shorter one, which must not be touched.
func withOverlap(t *testing.T) *Call {
	call := writable(t, true)
	for _, p := range [][2]string{{"orders", "Orders"}, {"orders-v2", "Orders v2"}, {"billing", "Billing"}} {
		blocks(t, handleUpsertProject, call, Args{"slug": p[0], "name": p[1]})
	}
	blocks(t, handleWriteMemory, call, Args{
		"id": "orders-only", "projects": []any{"orders"}, "type": "decision", "body": "Belongs to orders alone."})
	blocks(t, handleWriteMemory, call, Args{
		"id": "shared-fact", "projects": []any{"orders", "billing"}, "type": "gotcha", "body": "Both of them."})
	blocks(t, handleWriteMemory, call, Args{
		"id": "on-the-longer-slug", "projects": []any{"orders-v2"}, "type": "decision", "body": "The longer one."})
	blocks(t, handleWriteMemory, call, Args{
		"id": "points-at-it", "projects": []any{"billing"}, "type": "reference",
		"body": "See [[orders-only]] for why."})
	blocks(t, handleCommit, call, Args{"message": "save: the fixture"})
	return call
}

// Nothing happens on the first call. That is the whole contract: an agent has to
// show a person the plan, and until it comes back with the value it was given,
// the store is untouched.
func TestRenamePlansBeforeItActs(t *testing.T) {
	call := withOverlap(t)

	plan := blocks(t, handleRename, call, Args{"from": "orders", "to": "purchases"})
	if len(plan) != 3 {
		t.Fatalf("a plan returned %d blocks: %q", len(plan), plan)
	}
	if !strings.Contains(plan[0], "would rewrite the project directory") ||
		!strings.Contains(plan[0], "field of 2 memories") {
		t.Errorf("the plan reads:\n%s", plan[0])
	}
	if !strings.Contains(plan[1], "\"confirm\"") || !strings.Contains(plan[1], "orders-only") {
		t.Errorf("the plan carries:\n%s", plan[1])
	}
	if !strings.Contains(plan[2], "wait for an explicit yes") {
		t.Errorf("the third block is not the confirm note:\n%s", plan[2])
	}
	if !memory.ProjectExists(call.StoreDir, "orders") || memory.ProjectExists(call.StoreDir, "purchases") {
		t.Fatal("planning changed the store")
	}

	token := confirmFrom(t, plan[1])
	if said := refusal(t, handleRename, call, Args{
		"from": "orders", "to": "purchases", "confirm": "0000000000"}); !strings.Contains(said, "does not match") {
		t.Errorf("a wrong value was not refused clearly:\n%s", said)
	}

	done := blocks(t, handleRename, call, Args{"from": "orders", "to": "purchases", "confirm": token})
	if !strings.Contains(done[0], "Renamed 'orders' to 'purchases'. 2 memories retagged. Committed ") {
		t.Errorf("applying said:\n%s", done[0])
	}
	if !strings.Contains(done[1], "This store has no remote.") {
		t.Errorf("the second block reads %q", done[1])
	}

	// The longer slug and the prose are left alone.
	if got := projectsOf(t, call.StoreDir, "on-the-longer-slug"); got != "orders-v2" {
		t.Errorf("the longer slug was rewritten: %q", got)
	}
	if got := projectsOf(t, call.StoreDir, "shared-fact"); got != "purchases,billing" {
		t.Errorf("overlap did not survive: %q", got)
	}
	// Applying the same value twice cannot happen: the plan it described is gone.
	if said := refusal(t, handleRename, call, Args{
		"from": "orders", "to": "purchases", "confirm": token}); said == "" {
		t.Error("a second apply was accepted")
	}
}

// A memory shared with another project is untagged, never deleted. This is the
// promise that makes deleting a project safe at all.
func TestForgettingAProjectKeepsWhatIsShared(t *testing.T) {
	call := withOverlap(t)

	plan := blocks(t, handleForget, call, Args{"kind": "project", "target": "orders"})
	if len(plan) != 3 {
		t.Fatalf("a plan returned %d blocks: %q", len(plan), plan)
	}
	if !strings.Contains(plan[0], "deletes 1 memories tagged with it alone, and untags 1 shared") {
		t.Errorf("the plan reads:\n%s", plan[0])
	}
	for _, want := range []string{"\"deletes\"", "orders-only", "\"untags\"", "shared-fact", "\"brokenLinks\""} {
		if !strings.Contains(plan[1], want) {
			t.Errorf("the plan does not carry %s:\n%s", want, plan[1])
		}
	}

	done := blocks(t, handleForget, call, Args{
		"kind": "project", "target": "orders", "confirm": confirmFrom(t, plan[1])})
	body := done[0]
	if !strings.Contains(body, "Deleted project 'orders': 1 memories removed, 1 untagged and kept.") {
		t.Errorf("applying said:\n%s", body)
	}
	// Named, not counted: a number does not let anyone check the promise.
	if !strings.Contains(body, "Kept: shared-fact → billing") {
		t.Errorf("the surviving memory is not named:\n%s", body)
	}
	if !strings.Contains(body, "Now dangling, left as they are: memories/points-at-it.md") {
		t.Errorf("the dangling link is not reported:\n%s", body)
	}

	if _, err := os.Stat(memory.MemoryPath(call.StoreDir, "shared-fact")); err != nil {
		t.Error("a shared memory was deleted")
	}
	if got := projectsOf(t, call.StoreDir, "shared-fact"); got != "billing" {
		t.Errorf("untagging left %q", got)
	}
	if _, err := os.Stat(memory.MemoryPath(call.StoreDir, "orders-only")); !os.IsNotExist(err) {
		t.Error("the exclusive memory survived")
	}
	if memory.ProjectExists(call.StoreDir, "orders") {
		t.Error("the project directory survived")
	}
	// The link is reported, never repaired: editing the user's prose to hide the
	// gap would be worse than the gap.
	if pointing := read(t, memory.MemoryPath(call.StoreDir, "points-at-it")); !strings.Contains(pointing, "[[orders-only]]") {
		t.Error("the dangling link was silently rewritten")
	}
}

func TestForgettingOneMemory(t *testing.T) {
	call := withOverlap(t)

	plan := blocks(t, handleForget, call, Args{"kind": "memory", "target": "orders-only"})
	if !strings.Contains(plan[0], "Deleting memory 'orders-only' — tagged with orders.") {
		t.Errorf("the plan reads:\n%s", plan[0])
	}
	if !strings.Contains(plan[0], "\"Belongs to orders alone.\"") {
		t.Errorf("the plan does not quote the summary:\n%s", plan[0])
	}
	if !strings.Contains(plan[2], "link to it with [[orders-only]]") || !strings.Contains(plan[2], "NOT fixed silently") {
		t.Errorf("the third block does not warn about the link:\n%s", plan[2])
	}

	done := blocks(t, handleForget, call, Args{
		"kind": "memory", "target": "orders-only", "confirm": confirmFrom(t, plan[1])})
	if !strings.Contains(done[0], "Deleted memory 'orders-only'.") {
		t.Errorf("applying said:\n%s", done[0])
	}
	if _, err := os.Stat(memory.MemoryPath(call.StoreDir, "orders-only")); !os.IsNotExist(err) {
		t.Error("the memory survived")
	}
	if memory.ProjectExists(call.StoreDir, "orders") == false {
		t.Error("deleting a memory took its project with it")
	}
}

func TestForgettingWhatIsNotThere(t *testing.T) {
	call := withOverlap(t)

	if said := refusal(t, handleForget, call, Args{"kind": "project", "target": "nowhere"}); !strings.Contains(said, "orders") {
		t.Errorf("an unknown project does not name the real ones:\n%s", said)
	}
	if said := refusal(t, handleForget, call, Args{"kind": "memory", "target": "nothing"}); !strings.Contains(said, "Search for it") {
		t.Errorf("an unknown memory reads:\n%s", said)
	}
}

// confirmFrom reads the value out of a plan's JSON, the way an agent would.
func confirmFrom(t *testing.T, block string) string {
	t.Helper()
	_, after, found := strings.Cut(block, "\"confirm\": \"")
	if !found {
		t.Fatalf("no confirmation in:\n%s", block)
	}
	token, _, _ := strings.Cut(after, "\"")
	return token
}

func projectsOf(t *testing.T, dir, id string) string {
	t.Helper()
	m, ok := findMemory(t, dir, id)
	if !ok {
		t.Fatalf("memory %s is gone", id)
	}
	return strings.Join(m.Projects, ",")
}
