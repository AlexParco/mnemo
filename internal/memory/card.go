package memory

import (
	"fmt"
	"os"
	"strings"
)

// The resume card: what an agent prints when it loads a project. It answers one
// question, what is left to do here, in plain text with no icons and narrow
// enough for a small terminal.
//
// Everything else a project knows — decisions, constraints, gotchas, finished
// work — stays in the memory and is asked for when it is needed. Unfinished work
// is what gets lost between sessions, so it is what the card carries.
//
// The layout is in docs/specs/card.md, and the tests compare the render against
// expected files written from that document by hand.

const (
	// tasksShown is how many open tasks the card lists.
	tasksShown = 5

	headerWidth = 78
	itemWidth   = 68
)

// Lang is a supported output language.
type Lang string

const (
	EN Lang = "en"
	ES Lang = "es"
)

// ResolveLang turns a requested language into a supported one, defaulting to
// English. It reads no environment; the caller passes what the settings resolved.
func ResolveLang(value string) Lang {
	lang := strings.ToLower(strings.TrimSpace(value))
	if len(lang) > 2 {
		lang = lang[:2]
	}
	switch Lang(lang) {
	case EN, ES:
		return Lang(lang)
	}
	return EN
}

type words struct {
	todo, open, on, elsewhere, more, none, updated, note string
	status                                               map[string]string
}

var wordsFor = map[Lang]words{
	EN: {
		todo: "TODO", open: "open", on: "on", elsewhere: "elsewhere",
		more: "more", none: "none", updated: "updated", note: "note",
		status: map[string]string{"active": "active", "paused": "paused", "done": "done"},
	},
	ES: {
		todo: "PENDIENTES", open: "abiertas", on: "en", elsewhere: "en otras máquinas",
		more: "más", none: "ninguno", updated: "actualizado", note: "nota",
		status: map[string]string{"active": "activo", "paused": "pausado", "done": "hecho"},
	},
}

// ProjectNotFoundError is returned when the project has no INDEX.md.
type ProjectNotFoundError struct {
	Slug  string
	Store string
}

func (e *ProjectNotFoundError) Error() string {
	return fmt.Sprintf("project '%s' not found in %s", e.Slug, e.Store)
}

// CardOptions configure a render.
type CardOptions struct {
	Lang    Lang
	Machine string
	// Memories, when set, avoids re-reading the store.
	Memories []Memory
}

// task is one thing left to do, wherever it was written down.
type task struct {
	Text string
	// Origin is the pending section it came from, lower-cased, or the word for a
	// note when it came from a memory. Core sections leave it empty.
	Origin string
	// Machine is the label of the machine the task is bound to, or empty.
	Machine string
}

// RenderCard renders a project's card. The result has no trailing newline.
func RenderCard(store, slug string, opts CardOptions) (string, error) {
	indexRaw, err := os.ReadFile(IndexPath(store, slug))
	if err != nil {
		return "", &ProjectNotFoundError{Slug: slug, Store: store}
	}

	lang := opts.Lang
	if lang == "" {
		lang = EN
	}
	w := wordsFor[lang]

	pendingRaw, _ := os.ReadFile(PendingPath(store, slug)) // no pending.md is a valid state
	memories := opts.Memories
	if memories == nil {
		memories = LoadMemories(store)
	}

	head := header(ParseFields(string(indexRaw)), slug, w)
	tasks := openTasks(ParsePending(string(pendingRaw)), MemoriesForProject(memories, slug), w)
	return head + "\n\n" + taskBlock(tasks, NormalizeMachine(opts.Machine), w), nil
}

// header is the project's name and what describes it, in two lines.
func header(fields Fields, slug string, w words) string {
	title := slug
	if name := strings.TrimSpace(fields.Display("name", "")); name != "" && name != slug {
		title += " — " + name
	}

	status := fields.Display("status", "?")
	if translated, ok := w.status[status]; ok {
		status = translated
	}
	line := status
	if updated := strings.TrimSpace(fields.Display("updated", "")); updated != "" {
		line += " · " + w.updated + " " + updated
	}
	return title + "\n" + Truncate(line, headerWidth)
}

// openTasks collects everything left to do: the core sections first, then the
// project's own sections in file order, then the tasks written inside notes.
func openTasks(sections []Section, memories []Memory, w words) []task {
	var tasks []task
	add := func(text, origin string) {
		clean, machine := SplitStamp(text)
		tasks = append(tasks, task{Text: clean, Origin: origin, Machine: machine})
	}

	for _, keys := range [][]string{InProgress, NextUp} {
		for _, text := range OpenItems(sections, keys) {
			add(text, "")
		}
	}
	for _, section := range sections {
		if IsCoreSection(section.Key) {
			continue
		}
		for _, item := range section.Items {
			if !item.Done {
				add(item.Text, strings.ToLower(section.Label))
			}
		}
	}
	// A task written inside a note is still a task, and it is the one most easily
	// forgotten: nothing ever ticks it off. A note with an empty body is shown by
	// its id, which is always something, rather than as a blank line.
	for _, m := range memories {
		if m.Type != "todo" {
			continue
		}
		if m.Summary != "" {
			add(m.Summary, w.note)
		} else {
			add(m.ID, w.note)
		}
	}
	return tasks
}

func taskBlock(tasks []task, here string, w words) string {
	if len(tasks) == 0 {
		return w.todo + "  " + w.none
	}

	// Count every task bound elsewhere, not only the ones on show: work another
	// machine owns is exactly what should not be picked up here.
	away := map[string]int{}
	total := 0
	for _, t := range tasks {
		if t.Machine != "" && t.Machine != here {
			away[t.Machine]++
			total++
		}
	}
	head := fmt.Sprintf("%s  %d %s", w.todo, len(tasks), w.open)
	switch {
	case total == 0:
	case len(away) == 1:
		for machine := range away {
			head += fmt.Sprintf(" · %d %s %s", total, w.on, machine)
		}
	default:
		head += fmt.Sprintf(" · %d %s", total, w.elsewhere)
	}

	lines := []string{head}
	shown := tasks[:min(len(tasks), tasksShown)]
	for i, t := range shown {
		line := fmt.Sprintf("  %d  %s", i+1, Truncate(t.Text, itemWidth))
		if t.Origin != "" {
			line += "  " + t.Origin
		}
		if t.Machine != "" && t.Machine != here {
			line += "  " + t.Machine
		}
		lines = append(lines, line)
	}
	if left := len(tasks) - len(shown); left > 0 {
		lines = append(lines, fmt.Sprintf("  +%d %s", left, w.more))
	}
	return strings.Join(lines, "\n")
}
