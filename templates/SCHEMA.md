# Data contract

## Atomic memory — `memories/<id>.md`

```markdown
---
id: <unique-kebab-case>          # = file name without .md
projects: [<slug>, <slug2>]     # 1+ projects. THE overlap lives HERE.
services: [<optional>]          # areas/services/modules it touches (optional)
tags: [<optional>]              # free-form tags for search (optional)
type: decision | constraint | gotcha | bug | reference | todo
author: <name>
updated: <YYYY-MM-DD>
superseded_by: <id>             # optional: this memory was true, and <id> replaced it
---

Markdown body. One fact per file, self-explanatory and concise.
Link related memories with [[other-id]].
```

### Rules

- **`id` == file name.** `memories/checkout-customer-immutable.md` → `id: checkout-customer-immutable`.
- **`projects` is always a list.** Even for a single project: `projects: [shop-api]`.
  Filtering by project = "is the slug in `projects`?". The overlap comes for free.
- **One fact per file.** If a note mixes two topics, split it. This makes re-tagging and diffing easier.
- **Before creating, search for a duplicate** by `id`/topic. If it exists, update that file and its `updated`.
- **`updated` is the day this file was last written**, not the day the thing it describes happened,
  and not proof that it still holds. When a note and the code disagree, the code is what is true now
  and the note is what needs updating.
- **When a fact changes, supersede it, don't overwrite it.** Write the new memory with
  `supersedes: <old-id>`; the old file stays and gets `superseded_by: <new-id>`. Both remain
  searchable, and the order between them is readable. Deleting is for what should never have been
  written.
- **`type`**: `decision` (X was decided), `constraint` (invariant that must not be broken),
  `gotcha` (trap/non-obvious), `bug` (incident/known issue), `reference` (external pointer),
  `todo` (one-off pending item; the big stuff goes in `pending.md`).

## Project — `projects/<slug>/INDEX.md`

```markdown
---
slug: <kebab-case>
name: <readable name>
status: active | paused | done
services: [<areas it touches>]
updated: <YYYY-MM-DD>
---

# <name>

What the project is, its goal, its scope. Context that isn't derived from the code.
Services/areas it touches and why. Macro decisions with a link to [[memory-id]].
```

## Pending — `projects/<slug>/pending.md`

A living list of the **project state**. `/save-context` updates it; `/load-context` reads it to
resume. The sections are **free-form**: add only the ones that apply to the project. The resume
card lists the unchecked items of every section, `## In progress` and `## Next` first, each shown
with the section it came from. A ticked item never reaches the card.

```markdown
# Pending — <name>

## In progress
- [ ] <task> — <context/note>

## Next
- [ ] <task>

## Blocked                 <!-- optional -->
- [ ] <task> — blocked by <reason>

## <project-specific section>   <!-- optional, as many as you want -->
- [ ] <item>
```

Section names are free-form and bilingual: the card renders whatever exists, so old Spanish names
(`## En curso`, `## Siguiente`, `## Bloqueado`, …) still work. Each project picks the extra
sections according to what helps it resume. Examples by type of work: `Debt` (technical debt /
known issues), `Branches` (what was pushed to which branch), `Done`/`Deployed` (already closed,
with `- [x]` items, so you don't redo it), `Risks`, `Open decisions`… None are mandatory; add only
the ones that apply.

**Finishing something is written down.** Tick the item, `- [x]`, or move it to `## Done`. Never
delete it in the same session it was finished: the next session has no other way to know it happened,
and a task that quietly disappears gets proposed again. The pending list is what is left, not a
history.

**`## Done` is short on purpose.** It holds the last ten or so completions, not everything the
project ever did. When it grows past that, the older entries go, and before they go, each one is
worth one question: did this leave a rule, a constraint or a trap worth keeping? If it did, that
becomes a memory in `memories/`, where search will find it years later. If it did not, git already
has the commit and nobody will ask. Long lists of ticked items help nobody: the person reading the
card wants what is left, and the agent gets the count from the tools.

**Items tied to a machine.** Memory is shared across machines, but some items belong to just ONE
(local uncommitted code, "push repo X", a service running here). Stamp them with `[@<machine>]` at
the end (`<machine>` = `MNEMO_MACHINE` or the hostname). Seen from another machine, the resume card
shows which machine the item belongs to, and Claude won't try to act on it there. Portable tasks
(implement X, decisions) aren't stamped.
