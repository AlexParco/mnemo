package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/AlexParco/mnemo/internal/criterion"
	"github.com/AlexParco/mnemo/internal/store"
)

// The two tools that can lose memory.
//
// Both work in two calls. The first reports what would change and hands back a
// confirmation value; the second carries that value, and the store recomputes
// the plan and acts only if the two still match. So a plan that went stale, or
// a value that was never issued, cannot be applied.
//
// What no code here can enforce is that a person saw the plan and said yes.
// That lives in the confirm note, which is the third block of every plan.

type renamePlan struct {
	Memories []string `json:"memories"`
	Confirm  string   `json:"confirm"`
}

func handleRename(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	writable, err := call.writable()
	if err != nil {
		return a.from(err)
	}
	from, to, confirm := args.str("from"), args.str("to"), args.str("confirm")

	if confirm == "" {
		plan, err := writable.PlanRename(ctx, from, to)
		if err != nil {
			return a.from(err)
		}
		return a.say("Renaming '%s' to '%s' would rewrite the project directory, its INDEX.md, and the "+
			"`projects` field of %d memories.", from, to, len(plan.Memories)).
			data(renamePlan{Memories: nonNil(plan.Memories), Confirm: plan.Confirm}).
			block(criterion.ConfirmNote)
	}

	result, err := writable.ApplyRename(ctx, from, to, confirm)
	if err != nil {
		return a.from(err)
	}
	return a.say("Renamed '%s' to '%s'. %d memories retagged. Committed %s. From now on the project loads as '%s'.",
		result.From, result.To, len(result.MemoriesUpdated), result.Commit.SHA, result.To).
		block(publishedYet(result.Commit, "the other machines still have the old slug"))
}

// publishedYet is the line every destructive operation ends on. Committed is not
// published, and the other machines keep what was changed until it is.
func publishedYet(commit store.CommitResult, what string) string {
	if !commit.HasRemote {
		return "This store has no remote."
	}
	return "Local only — until this is pushed, " + what + "."
}

type forgetMemoryPlan struct {
	BrokenLinks []string `json:"brokenLinks"`
	Confirm     string   `json:"confirm"`
}

type untagged struct {
	ID        string   `json:"id"`
	Remaining []string `json:"remaining"`
}

type forgetProjectPlan struct {
	Deletes     []string   `json:"deletes"`
	Untags      []untagged `json:"untags"`
	BrokenLinks []string   `json:"brokenLinks"`
	Confirm     string     `json:"confirm"`
}

func handleForget(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	writable, err := call.writable()
	if err != nil {
		return a.from(err)
	}
	kind, target, confirm := args.str("kind"), args.str("target"), args.str("confirm")

	switch {
	case kind == "memory" && confirm == "":
		return planForgetMemory(ctx, a, writable, target)
	case kind == "memory":
		return applyForget(ctx, a, writable, target, confirm, false)
	case confirm == "":
		return planForgetProject(ctx, a, writable, target)
	default:
		return applyForget(ctx, a, writable, target, confirm, true)
	}
}

func planForgetMemory(ctx context.Context, a *answer, writable *store.Store, id string) *answer {
	plan, err := writable.PlanForgetMemory(ctx, id)
	if err != nil {
		return a.from(err)
	}
	tagged := "no project"
	if len(plan.Projects) > 0 {
		tagged = strings.Join(plan.Projects, ", ")
	}
	a.say("Deleting memory '%s' — tagged with %s.\n%q", id, tagged, plan.Summary)
	a.data(forgetMemoryPlan{BrokenLinks: nonNil(plan.BrokenLinks), Confirm: plan.Confirm})
	if len(plan.BrokenLinks) > 0 {
		// Reported, never repaired: a link that now goes nowhere is the user's to
		// decide about, and editing their prose to hide the gap would be worse.
		return a.say("%d file(s) link to it with [[%s]] and would be left dangling; they are NOT fixed silently. %s",
			len(plan.BrokenLinks), id, criterion.ConfirmNote)
	}
	return a.block(criterion.ConfirmNote)
}

func planForgetProject(ctx context.Context, a *answer, writable *store.Store, slug string) *answer {
	plan, err := writable.PlanForgetProject(ctx, slug)
	if err != nil {
		return a.from(err)
	}
	untags := make([]untagged, 0, len(plan.Shared))
	for _, shared := range plan.Shared {
		untags = append(untags, untagged{ID: shared.ID, Remaining: nonNil(shared.Remaining)})
	}
	return a.say("Deleting project '%s' (%s) removes projects/%s/ (INDEX.md, pending.md), deletes %d memories "+
		"tagged with it alone, and untags %d shared with other projects — those survive.",
		slug, plan.Name, slug, len(plan.Exclusive), len(plan.Shared)).
		data(forgetProjectPlan{
			Deletes: nonNil(plan.Exclusive), Untags: untags,
			BrokenLinks: nonNil(plan.BrokenLinks), Confirm: plan.Confirm,
		}).
		block(criterion.ConfirmNote)
}

func applyForget(ctx context.Context, a *answer, writable *store.Store, target, confirm string, project bool) *answer {
	var result store.ForgetResult
	var err error
	if project {
		result, err = writable.ApplyForgetProject(ctx, target, confirm)
	} else {
		result, err = writable.ApplyForgetMemory(ctx, target, confirm)
	}
	if err != nil {
		return a.from(err)
	}

	var lines []string
	if project {
		lines = append(lines, fmt.Sprintf("Deleted project '%s': %d memories removed, %d untagged and kept.",
			result.Target, len(result.Deleted), len(result.Untagged)))
	} else {
		lines = append(lines, fmt.Sprintf("Deleted memory '%s'.", result.Target))
	}
	lines = append(lines, fmt.Sprintf("Committed %s.", result.Commit.SHA))

	if len(result.Untagged) > 0 {
		// Named, not counted: the promise is that a shared memory survives, and a
		// number does not let anyone check it.
		var kept []string
		for _, shared := range result.Untagged {
			kept = append(kept, fmt.Sprintf("%s → %s", shared.ID, strings.Join(shared.Remaining, ", ")))
		}
		lines = append(lines, "Kept: "+strings.Join(kept, "; "))
	}
	if len(result.BrokenLinks) > 0 {
		lines = append(lines, "Now dangling, left as they are: "+strings.Join(result.BrokenLinks, ", "))
	}
	if result.Commit.HasRemote {
		lines = append(lines, "Local only — the other machines keep what was deleted until this is pushed.")
	}
	return a.block(strings.Join(lines, "\n"))
}
