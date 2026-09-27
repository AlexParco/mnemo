package mcpserver

import (
	"context"

	"strings"

	"github.com/AlexParco/mnemo/internal/criterion"
	"github.com/AlexParco/mnemo/internal/gitx"
)

// Talking to the hub.
//
// These four are one flow, not four tools: a sync can leave the store
// mid-rebase, and until a rebase is finished nothing else works. Every answer
// that leaves the store in that state says what to call next, because an agent
// that wanders off leaves the user's memory half-merged.

type conflictFile struct {
	File    string `json:"file"`
	Content string `json:"content"`
}

func handleSync(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	writable, err := call.writable()
	if err != nil {
		return a.from(err)
	}
	result, err := writable.Sync(ctx)
	if err != nil {
		return a.from(err)
	}

	if len(result.Conflicts) == 0 {
		if !result.Pulled && result.HasRemote && !result.Rebasing {
			// Git could not reach the hub, or refused. Reported as success the
			// agent tells the user their machines are in step when they are not.
			return a.refuse("Nothing was pulled, so this machine has not taken in what the others saved. %s",
				result.Detail)
		}
		return a.block(result.Detail)
	}

	files := make([]string, 0, len(result.Conflicts))
	carried := make([]conflictFile, 0, len(result.Conflicts))
	for _, conflict := range result.Conflicts {
		files = append(files, conflict.File)
		carried = append(carried, conflictFile{File: conflict.File, Content: conflict.Content})
	}
	// The conflicted files come back with the answer rather than being left for
	// the agent to read: it has to merge them, and a file it fetched separately
	// could already be a different one.
	return a.say("%s\n\n%d conflicted file(s): %s", result.Detail, len(files), strings.Join(files, ", ")).
		data(carried).
		block(criterion.ConflictRule)
}

func handleResolveConflict(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	writable, err := call.writable()
	if err != nil {
		return a.from(err)
	}
	file := args.str("file")
	remaining, err := writable.ResolveConflict(ctx, file, args.str("content"))
	if err != nil {
		return a.from(err)
	}

	if len(remaining) == 0 {
		return a.say("Resolved '%s'. Nothing left conflicted — call mnemo_rebase with action \"continue\".", file)
	}
	return a.say("Resolved '%s'. Still conflicted: %s.", file, strings.Join(remaining, ", "))
}

func handleRebase(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	writable, err := call.writable()
	if err != nil {
		return a.from(err)
	}
	result, err := writable.Rebase(ctx, gitx.RebaseAction(args.str("action")))
	if err != nil {
		return a.from(err)
	}
	if !result.OK {
		// Continuing with files still conflicted is the common mistake, and the
		// detail names them. It is advice, not a fault.
		return a.refuse("%s", result.Detail)
	}
	if gitx.RebaseAction(args.str("action")) == gitx.Abort {
		// "The store is as it was" is true of the state before the sync and a lie
		// about the session: the hub's memory is still unmerged and the same
		// conflict comes back next time.
		return a.say("%s\n\nWhat the hub sent is not merged: the next mnemo_sync will raise the same conflict. "+
			"Tell the user in plain words that their other machines' memory has not arrived yet, and do not sync "+
			"again until they have said how the two sides should merge.", result.Detail)
	}
	return a.block(result.Detail)
}

type finding struct {
	Rule    string `json:"rule"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Excerpt string `json:"excerpt"`
}

func handlePush(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	writable, err := call.writable()
	if err != nil {
		return a.from(err)
	}
	result, err := writable.Push(ctx, args.str("acknowledge"))
	if err != nil {
		return a.from(err)
	}

	switch result.Refused {
	case "":
		if !result.Pushed {
			// git push failed for a reason gitx does not classify: unreachable
			// hub, rejected ref, no credentials. Nothing left this machine.
			return a.refuse("Nothing was published, so the user's other machines still cannot see this. %s",
				result.Detail)
		}
	case gitx.RefusedNothingToPush:
		// Nothing to do is not something gone wrong: the agent asked for the
		// work to be published and it already is.
		return a.block(result.Detail)
	case gitx.RefusedSecrets, gitx.RefusedBadAcknowledge:
		// Three blocks: what happened, what was found, and what to do. The
		// excerpts never carry the value, so showing them to the user is safe.
		findings := make([]finding, 0, len(result.Findings))
		for _, f := range result.Findings {
			findings = append(findings, finding{Rule: f.Rule, File: f.File, Line: f.Line, Excerpt: f.Excerpt})
		}
		a.say("%s", result.Detail)
		a.data(findings)
		return a.say("Nothing was pushed, and nothing you can do here will push it until a person has looked at "+
			"these findings. Show them to the user now, in this conversation, and stop. Only after they have "+
			"said in their own words that these are false positives, call mnemo_push again with acknowledge: "+
			"%q. Do not open the files and decide for yourself — you cannot tell a test fixture from the "+
			"user's live credential — and do not send that value back in the same turn you received it. If the "+
			"user does not answer, leave the work committed and tell them it is not on the hub yet: a "+
			"credential published to a hub cannot be taken back, a delayed push can.", result.Acknowledge)
	default:
		// No hub, or a rebase still in progress: nothing was published and
		// there is something to fix first.
		return a.refuse("%s", result.Detail)
	}
	return a.block(result.Detail)
}
