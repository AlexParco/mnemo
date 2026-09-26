package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/AlexParco/mnemo/internal/store"
)

// The tools that change the store.
//
// None of them commits by itself. A session writes what it produced and then
// calls mnemo_commit once, so one session is one commit and the history reads
// as a record of sessions rather than of keystrokes. Pushing is separate again.

// errNotWritable is for a context that has no store behind it.
var errNotWritable = errors.New(
	"this connection can read the memory but not change it. Nothing was written")

// errNowhere is for a store with no path. Every path it touched would be
// relative to whatever directory this process was started in, which for a
// server started by a coding tool is the user's own repository.
var errNowhere = errors.New("mnemo does not know where the store should be, so it will not write one. " +
	"Nothing was written. Set MNEMO_DIR, or store.dir in the config file")

// notCommitted is said after every write, because the alternative — an agent
// assuming its work is safe — ends with the work on no machine at all.
const notCommitted = "Not committed yet — this is normal. Keep writing, and call mnemo_commit ONCE when the " +
	"session's writes are all done. Do not commit after each write."

// from turns an error from the store into an answer. A refusal is advice the
// agent can act on and is handed over as written; anything else is a fault of
// mnemo's and must not be dressed up as advice.
func (a *answer) from(err error) *answer {
	if store.IsRefusal(err) || errors.Is(err, errNotWritable) || errors.Is(err, errNowhere) {
		return a.refuse("%s", err.Error())
	}
	// A path that cannot be read or written is the machine's state, not a bug
	// in mnemo: the user can chmod it or point mnemo elsewhere. Calling it an
	// unexpected failure tells the agent there is nothing to be done.
	var path *fs.PathError
	if errors.As(err, &path) {
		return a.refuse("mnemo cannot use the store where it is: %v. Fix the path, or point mnemo somewhere "+
			"else with MNEMO_DIR. Nothing was written.", err)
	}
	return a.fail("%v", err)
}

func handleBootstrap(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	writable, err := call.writable()
	if err != nil {
		return a.from(err)
	}
	report, err := writable.Ensure(ctx)
	if err != nil {
		return a.from(err)
	}

	var lines []string
	if report.CreatedStore {
		created := fmt.Sprintf("Created the store at %s.", report.Store)
		if report.WiredRemote == "" && !report.AdoptedHistory {
			// Only worth saying when there is no hub. Printed next to "Wired
			// remote" it would be advice contradicting the line below it.
			created += " Set a hub remote with mnemo config set store.remote <url> to sync it across machines."
		}
		lines = append(lines, created)
	}
	switch {
	case report.AdoptedHistory:
		lines = append(lines, "Adopted the existing memory from the hub — this machine now shares it.")
	case report.WiredRemote != "":
		lines = append(lines, "Wired remote: "+report.WiredRemote)
	}
	if report.AdoptionBlocked != "" {
		lines = append(lines, "warning: "+report.AdoptionBlocked)
	}
	if len(report.WroteTemplates) > 0 {
		lines = append(lines, "Wrote: "+strings.Join(report.WroteTemplates, ", "))
	}
	if !report.HasIdentity {
		// Said here rather than at the first commit, because a machine set up
		// today and used tomorrow should learn this while someone is watching.
		lines = append(lines, "warning: git has no committer identity here; "+
			"set user.name and user.email or commits will fail.")
	}

	if len(lines) == 0 {
		return a.say("The store at %s was already set up; nothing to do.", report.Store)
	}
	return a.block(strings.Join(lines, "\n"))
}

func handleUpsertProject(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	writable, err := call.writable()
	if err != nil {
		return a.from(err)
	}

	in := store.ProjectInput{
		Slug:     args.str("slug"),
		Name:     args.str("name"),
		Status:   args.str("status"),
		Services: args.list("services"),
	}
	// A description that was not sent is left alone; one sent empty clears it.
	if args.has("description") {
		description := args.str("description")
		in.Description = &description
	}

	result, err := writable.UpsertProject(ctx, in)
	if err != nil {
		return a.from(err)
	}

	first := fmt.Sprintf("Updated project '%s' (%s).", result.Slug, result.Path)
	if result.Created {
		first = fmt.Sprintf("Created project '%s' (%s).", result.Slug, result.Path) +
			" Its pending.md is empty; sections are free-form, add only the ones that apply."
	}
	return a.block(first).say(notCommitted)
}

func handleWriteMemory(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	writable, err := call.writable()
	if err != nil {
		return a.from(err)
	}

	result, err := writable.WriteMemory(ctx, store.MemoryInput{
		ID:         args.str("id"),
		Projects:   args.list("projects"),
		Type:       args.str("type"),
		Body:       args.str("body"),
		Services:   args.list("services"),
		Tags:       args.list("tags"),
		Author:     args.str("author"),
		Overwrite:  args.flag("overwrite"),
		Supersedes: args.str("supersedes"),
	})
	if err != nil {
		return a.from(err)
	}

	verb := "Updated"
	if result.Created {
		verb = "Wrote"
	}
	a.say("%s memory '%s' (%s). %s", verb, result.ID, result.Path, notCommitted)
	if result.Superseded != "" {
		a.say("'%s' is kept and marked as superseded by this one. Nothing was deleted: someone asking "+
			"why the answer changed needs both.", result.Superseded)
	}
	if len(result.Related) > 0 {
		// Near-duplicates are reported, never merged: only the agent knows
		// whether two notes are one fact.
		a.say("These existing memories share vocabulary with it — check you are not splitting one fact in two: %s",
			strings.Join(result.Related, ", "))
	}
	return a
}

type pendingSection struct {
	Label string `json:"label"`
	Open  int    `json:"open"`
	Done  int    `json:"done"`
}

func handleWritePending(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	writable, err := call.writable()
	if err != nil {
		return a.from(err)
	}

	result, err := writable.WritePending(ctx, args.str("slug"), args.str("content"))
	if err != nil {
		return a.from(err)
	}

	sections := make([]pendingSection, 0, len(result.Sections))
	for _, s := range result.Sections {
		sections = append(sections, pendingSection{Label: s.Label, Open: s.Open, Done: s.Done})
	}
	// What mnemo parsed, handed back: the file was replaced wholesale, and an
	// agent that meant to write four sections needs to see four.
	return a.say("Rewrote pending.md for '%s'. %s", result.Slug, notCommitted).data(sections)
}

func handleCommit(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	writable, err := call.writable()
	if err != nil {
		return a.from(err)
	}

	result, err := writable.Commit(ctx, args.str("message"))
	if err != nil {
		return a.from(err)
	}
	if !result.Committed {
		return a.say("Nothing to commit: the store has no pending changes.")
	}

	lines := []string{fmt.Sprintf("Committed %s · %d file(s):", result.SHA, len(result.Files))}
	for _, file := range result.Files {
		lines = append(lines, "  "+file)
	}
	if result.StrippedTrailers > 0 {
		lines = append(lines, fmt.Sprintf("Removed %d Co-Authored-By trailer(s): the store does not carry them.",
			result.StrippedTrailers))
	}
	// Committed is not published. An agent that stops here has left the work on
	// one machine, and the user will not find it on the other.
	if !result.HasRemote {
		lines = append(lines, "This store has no remote, so it lives only on this machine.")
	} else {
		if result.UnpushedKnown {
			lines = append(lines, fmt.Sprintf("Local only. %d commit(s) not on the hub yet — "+
				"the other machines will not see this until it is pushed.", result.Unpushed))
		} else {
			// Never print a fake number. "some commit(s)" reads like a bug, and
			// the reason the count is missing is itself worth saying.
			lines = append(lines, "Local only. Commits are not on the hub yet (no upstream branch, so the count "+
				"is unknown) — the other machines will not see this until it is pushed.")
		}
	}
	return a.block(strings.Join(lines, "\n"))
}
