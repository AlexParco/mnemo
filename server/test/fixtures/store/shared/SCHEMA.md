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
---

Markdown body. One fact per file, self-explanatory and concise.
Link related memories with [[other-id]].
```

### Rules

- **`id` == file name.** `memories/checkout-customer-immutable.md` → `id: checkout-customer-immutable`.
- **`projects` is always a list.** Even for a single project: `projects: [rappi-f3]`.
  Filtering by project = "is the slug in `projects`?". The overlap comes for free.
- **One fact per file.** If a note mixes two topics, split it. This makes re-tagging and diffing easier.
- **Before creating, search for a duplicate** by `id`/topic. If it exists, update that file and its `updated`.
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
resume. The sections are **free-form**: the `load-context` card renders whichever ones exist, so
add only the ones that apply to the project. `## In progress` and `## Next` feed the numbered
"Pending" items; the rest (`Blocked`, `Debt`, `Deployed`, …) are shown as blocks.

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

**Items tied to a machine.** Memory is shared across machines, but some items belong to just ONE
(local uncommitted code, "push repo X", a service running here). Stamp them with `[@<machine>]` at
the end (`<machine>` = `MNEMO_MACHINE` or the hostname). Seen from another machine, `load-context`
marks them with **⚠** and Claude won't try to act on them there. Portable tasks (implement X,
decisions) aren't stamped.
