package mcpserver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/AlexParco/mnemo/internal/criterion"
	"github.com/AlexParco/mnemo/internal/memory"
)

// The read-only tools. None of them writes, none of them takes the lock, and
// none of them creates the store: a machine that has not saved yet gets an
// answer that says so, not a store it did not ask for.

// noStore is what an agent is told when there is nothing to read. It says how a
// store comes into being, because the alternative reading — that the user's
// memory is empty — is wrong and an agent will act on it.
const noStore = "There is no mnemo store yet. It is created by the first save (see mnemo's save-context flow). " +
	"To adopt an existing store from another machine, set the hub remote with mnemo config set store.remote <url> " +
	"before the first save."

// midMerge is what the tools that return memory say while a rebase is in
// progress.
//
// They refuse rather than answer. The files on disk carry conflict markers at
// that point, and the parsers read them as ordinary markdown — so a card an
// agent is told to print verbatim could show a user half a merge as fact. It is
// also what makes the sync tool's promise true: until the rebase is finished,
// nothing else works.
const midMerge = "This store is mid-merge: a sync left a rebase in progress, so the files carry conflict markers " +
	"and anything read from them now would be half a merge. Call mnemo_sync to get the conflicted files with " +
	"their contents, send each one back merged with mnemo_resolve_conflict, then call mnemo_rebase with action " +
	"\"continue\". mnemo_status says where things stand."

// settled reports whether the store can be read from, and the refusal when not.
func (c *Call) settled() (bool, string) {
	if c.storeExists() && c.repo().Status().Rebasing {
		return false, midMerge
	}
	return true, ""
}

// unknownProject names the slugs that do exist. An agent that guessed a slug
// needs the real ones, and an agent that invented one needs to be stopped.
func unknownProject(storeDir, slug string) string {
	lines := []string{fmt.Sprintf("No project '%s' in the store.", slug)}
	if slugs := memory.ProjectSlugs(storeDir); len(slugs) > 0 {
		lines = append(lines,
			"Existing slugs: "+strings.Join(slugs, ", "),
			"Pick one; do not guess or invent a project.")
	} else {
		lines = append(lines, "The store has no projects yet.")
	}
	return strings.Join(lines, "\n")
}

func handleStatus(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	exists := call.storeExists()

	lines := []string{"store: " + call.StoreDir}
	if !exists {
		lines[0] += "  (does not exist yet)"
	}
	if call.ServedBy != "" {
		lines = append(lines, "served by: "+call.ServedBy)
	}
	lines = append(lines, "machine: "+call.Machine)

	if exists {
		status := call.repo().Status()
		lines = append(lines, "git repo: "+yesNo(status.IsRepo))
		if status.Remote != "" {
			lines = append(lines, "remote: "+status.Remote)
		} else {
			lines = append(lines, "remote: none — this store is local only")
		}
		lines = append(lines, "branch: "+orQuestion(status.Branch))
		lines = append(lines, "uncommitted changes: "+yesNo(status.Dirty))
		if status.UnpushedKnown {
			lines = append(lines, fmt.Sprintf("unpushed commits: %d", status.Unpushed))
		} else {
			lines = append(lines, "unpushed commits: unknown (no upstream)")
		}
		if status.Rebasing {
			// The one line that tells a fresh session it has to finish something
			// before anything else works, so it names what to call.
			lines = append(lines, "warning: a rebase is in progress — this store is half-merged and what you "+
				"read from it may contain conflict markers. Call mnemo_sync for the conflicted files with their "+
				"contents, send each one back with mnemo_resolve_conflict, then call mnemo_rebase with action "+
				"\"continue\". Nothing can be written until that finishes.")
		}
		overview := memory.BuildOverview(call.StoreDir)
		lines = append(lines, fmt.Sprintf("projects: %d · memories: %d",
			overview.Totals.Projects, overview.Totals.Memories))
	}

	if call.Autopush {
		lines = append(lines, "autopush: on — the user has opted into pushing without being asked each time")
	} else {
		lines = append(lines, "autopush: off — confirm with the user before calling mnemo_push")
	}
	if !exists {
		lines = append(lines, "", noStore)
		return a.block(strings.Join(lines, "\n"))
	}

	slug := args.str("slug")
	if slug == "" {
		return a.block(strings.Join(lines, "\n"))
	}
	project, ok := memory.ReadProject(call.StoreDir, slug)
	if !ok {
		return a.block(strings.Join(lines, "\n")).block(unknownProject(call.StoreDir, slug))
	}
	lines = append(lines, "")
	lines = append(lines, projectLines(call, project)...)
	return a.block(strings.Join(lines, "\n")).
		say("For the full picture — every memory, the conventions, the printable card — use mnemo_load_project.")
}

// projectLines is the compact summary of one project, for re-checking mid-session
// without paying for a whole load.
func projectLines(call *Call, project memory.Project) []string {
	memories := memory.MemoriesForProject(memory.LoadMemories(call.StoreDir), project.Slug)
	shared := 0
	for _, m := range memories {
		if len(m.Projects) > 1 {
			shared++
		}
	}

	lines := []string{
		fmt.Sprintf("project: %s · %s", project.Slug, project.Status),
		"name: " + project.Name,
	}
	if len(project.Services) > 0 {
		lines = append(lines, "services: "+strings.Join(project.Services, ", "))
	}
	if project.Updated != "" {
		lines = append(lines, "updated: "+project.Updated)
	}
	count := fmt.Sprintf("memories: %d", len(memories))
	if shared > 0 {
		count += fmt.Sprintf(" (%d shared with other projects)", shared)
	}
	lines = append(lines, count)

	inProgress := memory.OpenItems(project.Pending, memory.InProgress)
	next := memory.OpenItems(project.Pending, memory.NextUp)
	open, elsewhere, done := 0, 0, 0
	for _, section := range project.Pending {
		for _, item := range section.Items {
			if item.Done {
				done++
				continue
			}
			open++
			if _, machine := memory.SplitStamp(item.Text); machine != "" && machine != call.Machine {
				elsewhere++
			}
		}
	}
	pending := fmt.Sprintf("pending: %d open — %d in progress, %d next", open, len(inProgress), len(next))
	if elsewhere > 0 {
		pending += fmt.Sprintf(" · %s", plural(elsewhere, "belongs", "belong")+" to another machine")
	}
	lines = append(lines, pending)
	if done > 0 {
		// Said here and nowhere else: the card does not show finished work, and
		// an agent that reads the card as a history concludes it was skipped.
		lines = append(lines, fmt.Sprintf("done: %d ticked in the pending list — the card does not show them", done))
	}

	resume := "—"
	if len(inProgress) > 0 {
		resume, _ = memory.SplitStamp(inProgress[0])
	} else if len(next) > 0 {
		resume, _ = memory.SplitStamp(next[0])
	}
	return append(lines, "next: "+resume)
}

func handleListProjects(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	if !call.storeExists() {
		return a.refuse("%s\nExpected location: %s", noStore, call.StoreDir)
	}

	overview := memory.BuildOverview(call.StoreDir)
	if len(overview.Projects) == 0 {
		return a.say("The store has no projects yet. Ask the user which project to create, then create it with " +
			"mnemo_upsert_project: a write to a slug that does not exist is refused, never created.")
	}

	rows := []string{
		"| project (slug) | status | memories | services | updated |",
		"|---|---|---|---|---|",
	}
	for _, p := range overview.Projects {
		count := fmt.Sprintf("%d", p.Memories)
		if p.Shared > 0 {
			count += fmt.Sprintf(" (%d shared)", p.Shared)
		}
		rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s | %s |",
			p.Slug, p.Status, count, orDash(strings.Join(p.Services, ", ")), orDash(p.Updated)))
	}

	summary := fmt.Sprintf("%d projects · %d memories · %d files in shared/",
		overview.Totals.Projects, overview.Totals.Memories, overview.Totals.SharedFiles)
	if overview.Totals.OrphanMemories > 0 {
		summary += fmt.Sprintf(" · %d orphan (tagged to no existing project)", overview.Totals.OrphanMemories)
	}
	return a.block(strings.Join(rows, "\n") + "\n\n" + summary)
}

// How much of a project comes back in one answer. A store grows without limit
// and a project can hold hundreds of memories, so returning every body is a
// promise that breaks quietly on the day it matters most. The index is always
// complete; only bodies are rationed, and what was left out is said out loud.
const (
	bodyBudget   = 32 * 1024
	sharedBudget = 8 * 1024
	// doneBudget is how many ticked items come back. Finished work is never
	// deleted from a pending list — that record is what stops the next session
	// proposing a task that was already done — so the list grows without limit
	// and it is the answer that is bounded, not the file. The most recent ones
	// are the ones an agent is about to need.
	doneBudget = 20
	// pendingBudget caps the copy of pending.md that comes back for rewriting.
	// Beyond it the copy is marked as cut, and an agent is told not to send it
	// back: a partial file written whole would delete the rest.
	pendingBudget = 64 * 1024
)

type detailProject struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Services    []string `json:"services"`
	Updated     string   `json:"updated"`
	Description string   `json:"description"`
}

type detailItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

type detailSection struct {
	Key   string       `json:"key"`
	Label string       `json:"label"`
	Items []detailItem `json:"items"`
}

type detailMemory struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Projects []string `json:"projects"`
	Services []string `json:"services"`
	Tags     []string `json:"tags"`
	Updated  string   `json:"updated"`
	Summary  string   `json:"summary"`
	Body     string   `json:"body,omitempty"`
	// BodyOmitted marks an entry whose body did not fit. It is never absent
	// quietly: an agent has to be able to tell "no body" from "not sent".
	BodyOmitted bool `json:"body_omitted,omitempty"`
}

type detailShared struct {
	Name      string `json:"name"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

type detail struct {
	Project detailProject `json:"project"`
	// PendingFile is pending.md exactly as it is on disk, because the tool that
	// rewrites it replaces the whole file. The parsed sections below are a
	// summary — they keep headings and checkbox lines only, drop prose and cut
	// each item at 100 characters — so an agent that rebuilt the file from them
	// would silently delete the user's own words.
	PendingFile string `json:"pending_file"`
	// PendingTruncated says the file above was cut and must not be sent back.
	PendingTruncated bool            `json:"pending_truncated,omitempty"`
	Pending          []detailSection `json:"pending"`
	Memories         []detailMemory  `json:"memories"`
	Shared           []detailShared  `json:"shared"`
}

func handleLoadProject(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	if settled, why := call.settled(); !settled {
		return a.refuse("%s", why)
	}
	if !call.storeExists() {
		return a.refuse("%s\nExpected location: %s", noStore, call.StoreDir)
	}
	slug := args.str("slug")
	lang := call.language(args.str("lang"))

	loaded, ok := memory.LoadProjectContext(call.StoreDir, slug, lang, call.Machine)
	if !ok {
		return a.refuse("%s", unknownProject(call.StoreDir, slug))
	}
	a.block(loaded.Card)

	if args.choice("detail", "full") == "full" {
		body, omitted := buildDetail(call.StoreDir, loaded)
		// The label and the JSON are one block, so that what was left out can be
		// the third and the machine rule stays last. An agent is told to print
		// the first block and keep the rest; the fewer of them, the clearer that
		// instruction is.
		encoded, err := encode(body)
		if err != nil {
			return a.fail("%v", err)
		}
		a.block("Detail (do not print unless asked):\n" + encoded)
		a.block(omitted)
	}
	return a.block(machineBlock(call, loaded))
}

// buildDetail assembles the second block and the sentence that says what was
// left out of it.
func buildDetail(storeDir string, loaded memory.ProjectContext) (detail, string) {
	project := loaded.Project
	out := detail{
		Project: detailProject{
			Slug: project.Slug, Name: project.Name, Status: project.Status,
			Services: nonNil(project.Services), Updated: project.Updated, Description: project.Description,
		},
		Pending:  []detailSection{},
		Memories: []detailMemory{},
		Shared:   []detailShared{},
	}
	if raw, err := os.ReadFile(memory.PendingPath(storeDir, project.Slug)); err == nil {
		out.PendingFile = string(raw)
		if len(out.PendingFile) > pendingBudget {
			out.PendingFile, out.PendingTruncated = out.PendingFile[:pendingBudget], true
		}
	}

	// Every unchecked item comes back, always: they are the work. Ticked ones
	// are capped, counting from the end of the file, which is where the recent
	// ones are.
	ticked := 0
	for _, section := range project.Pending {
		for _, item := range section.Items {
			if item.Done {
				ticked++
			}
		}
	}
	skipDone := ticked - doneBudget
	if skipDone < 0 {
		skipDone = 0
	}
	dropped := skipDone
	for _, section := range project.Pending {
		items := make([]detailItem, 0, len(section.Items))
		for _, item := range section.Items {
			if item.Done && skipDone > 0 {
				skipDone--
				continue
			}
			items = append(items, detailItem{Text: item.Text, Done: item.Done})
		}
		out.Pending = append(out.Pending, detailSection{Key: section.Key, Label: section.Label, Items: items})
	}

	spent, withoutBody := 0, 0
	for _, m := range memory.MemoriesForProject(loaded.Memories, project.Slug) {
		entry := detailMemory{
			ID: m.ID, Type: m.Type, Projects: nonNil(m.Projects), Services: nonNil(m.Services),
			Tags: nonNil(m.Tags), Updated: m.Updated, Summary: m.Summary,
		}
		if spent+len(m.Body) <= bodyBudget {
			entry.Body = m.Body
			spent += len(m.Body)
		} else {
			entry.BodyOmitted = true
			withoutBody++
		}
		out.Memories = append(out.Memories, entry)
	}

	cut := 0
	for _, file := range loaded.Shared {
		entry := detailShared{Name: file.Name, Content: file.Content}
		if len(entry.Content) > sharedBudget {
			entry.Content, entry.Truncated = entry.Content[:sharedBudget], true
			cut++
		}
		out.Shared = append(out.Shared, entry)
	}

	var left []string
	if withoutBody > 0 || cut > 0 {
		left = append(left, fmt.Sprintf(
			"%d of %d memories came back without their body, and %d shared file(s) were cut. "+
				"Read any of them in full with mnemo_read_memory.", withoutBody, len(out.Memories), cut))
	}
	if out.PendingTruncated {
		left = append(left, "pending.md was too long to send back in full, so `pending_file` is cut. Do not rebuild "+
			"the file from it: tell the user, and leave the pending list alone this time.")
	}
	if dropped > 0 {
		left = append(left, fmt.Sprintf(
			"%d older ticked item(s) are not in the pending sections above; the %d most recent are. "+
				"Nothing was deleted — the file has them all.", dropped, doneBudget))
	}
	return out, strings.Join(left, "\n")
}

// machineBlock is the third block: whose machine this is, what is not theirs to
// act on, and what the card is not showing.
func machineBlock(call *Call, loaded memory.ProjectContext) string {
	elsewhere, done := 0, 0
	for _, section := range loaded.Project.Pending {
		for _, item := range section.Items {
			if item.Done {
				done++
				continue
			}
			if _, machine := memory.SplitStamp(item.Text); machine != "" && machine != call.Machine {
				elsewhere++
			}
		}
	}

	parts := []string{fmt.Sprintf("This machine is '%s'. %d pending item(s) belong to another machine, "+
		"and the card shows which one.", call.Machine, elsewhere)}
	if done > 0 {
		// The card is a list of what is left. An agent that reads it as a record
		// of the session will propose finished work again, which is the failure
		// this sentence exists to prevent.
		parts = append(parts, fmt.Sprintf("%d item(s) are already done. The card lists what is left, never what is "+
			"finished, so do not read it as a history: a task that is not on it may have been done, not skipped. "+
			"The done ones are in the detail above.", done))
	}
	return strings.Join(append(parts, criterion.MachineRule), "\n\n")
}

type searchHit struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Projects []string `json:"projects"`
	Services []string `json:"services"`
	Updated  string   `json:"updated"`
	Summary  string   `json:"summary"`
}

func handleSearch(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	if settled, why := call.settled(); !settled {
		return a.refuse("%s", why)
	}
	if !call.storeExists() {
		return a.refuse("%s\nExpected location: %s", noStore, call.StoreDir)
	}

	limit := args.num("limit", memory.DefaultSearchLimit)
	found := memory.Search(call.StoreDir, memory.SearchOptions{
		Query:   args.str("query"),
		Project: args.str("project"),
		Type:    args.str("type"),
		Limit:   limit,
	})
	if len(found) == 0 {
		// The most useful thing an empty search can do is stop the agent from
		// answering from memory of its own.
		return a.say("No memories matched. Nothing is saved about this, and that is the answer — say so rather " +
			"than filling the gap from what you recall. A narrow query can also be the reason: widen it, or list " +
			"the projects, before concluding that the store is empty on the subject.")
	}

	hits := make([]searchHit, 0, len(found))
	for _, m := range found {
		hits = append(hits, searchHit{
			ID: m.ID, Type: m.Type, Projects: nonNil(m.Projects), Services: nonNil(m.Services),
			Updated: m.Updated, Summary: m.Summary,
		})
	}
	a.data(hits).say("Use mnemo_read_memory for the full body of any of these.")
	if len(found) == limit {
		// A result that was cut must never look like a complete one.
		a.say("Stopped at %d results; there may be more. Narrow the query or raise the limit.", limit)
	}
	return a
}

type memoryFields struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Projects []string `json:"projects"`
	Services []string `json:"services"`
	Tags     []string `json:"tags"`
	Updated  string   `json:"updated"`
	Author   string   `json:"author"`
}

func handleReadMemory(ctx context.Context, call *Call, args Args) *answer {
	a := &answer{}
	if settled, why := call.settled(); !settled {
		return a.refuse("%s", why)
	}
	id := args.str("id")
	if !call.storeExists() {
		return a.refuse("%s\nExpected location: %s", noStore, call.StoreDir)
	}
	for _, m := range memory.LoadMemories(call.StoreDir) {
		if m.ID != id {
			continue
		}
		return a.block(m.Raw).data(memoryFields{
			ID: m.ID, Type: m.Type, Projects: nonNil(m.Projects), Services: nonNil(m.Services),
			Tags: nonNil(m.Tags), Updated: m.Updated, Author: m.Author,
		})
	}
	return a.refuse("No memory '%s'. Search for it with mnemo_search_memories before assuming it does not exist.", id)
}

// rulesFile is what the guide is destined for, which changes only the sentence
// above it.
var rulesFile = map[string]string{
	"agents":  "AGENTS.md",
	"claude":  "CLAUDE.md",
	"generic": "your agent's rules file",
}

func handleGuide(ctx context.Context, call *Call, args Args) *answer {
	target := rulesFile[args.choice("target", "generic")]
	if target == "" {
		target = rulesFile["generic"]
	}
	return (&answer{}).
		say("Paste the following into %s:", target).
		block(criterion.Guide)
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func orQuestion(value string) string {
	if value == "" {
		return "?"
	}
	return value
}

func orDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// nonNil keeps an empty list out of the JSON as `[]` rather than `null`, so an
// agent reading the answer never has to tell the two apart.
func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
