# Resume card

The card is what an agent prints when it loads a project. It answers one question: what is left to
do here. It is plain text, it has no icons, and it fits a narrow terminal.

Nothing else goes on it. Decisions, constraints, gotchas and finished work are in the memory and are
asked for when they are needed; unfinished work is the thing that gets lost between sessions.

**The card is printed to a person, and that is why finished work is not on it.** Nobody wants to
read fifty ticked lines to find the two that are left. The agent is a different reader with a
different problem: it must not mistake this list for a history, because a task that is absent may
have been done rather than skipped. So the count of what is finished, and that warning, travel in
the blocks that are not printed — see [mcp.md](mcp.md#mnemo_load_project) and `mnemo_status`.

It is generated, so its layout is a contract: the tests compare the render against expected files
kept in the repository.

## Inputs

- The store and a slug. A project without `INDEX.md` is an error.
- The language: the tool's `lang` argument, else the resolved `lang` setting, else `en`.
- The machine: the caller's machine label. Over a server that is the client's machine, not the
  server's, so an item bound to the laptop is not marked when the laptop loads it.

## Shape

```
<slug>[ — <name>]
<status>[ · <updated label> <date>]

<TODO>  <n> <open>[ · <k> <on> <machine>]
  1  <item>[  <origin>][  <machine>]
  2  <item>[  <origin>][  <machine>]
  3  <item>[  <origin>][  <machine>]
  4  <item>[  <origin>][  <machine>]
  5  <item>[  <origin>][  <machine>]
  +<n> <more>
```

The two blocks are separated by one blank line. The card has no trailing newline.

## What counts as an open task

Three sources, in this order:

1. **The core sections of `pending.md`**: the unchecked items of `## In progress` and `## En curso`,
   then those of `## Next` and `## Siguiente`. These have no origin column.
2. **Every other section of `pending.md`**, in the order the sections appear in the file: their
   unchecked items, with the section's label, lower-cased, as their origin.
3. **The memories of the project whose type is `todo`**, in file order, with `note` as their origin.
   A task written inside a note is still a task, and it is the one most easily forgotten. The text is
   the note's summary, or its id when the note has an empty body.

Checked items never appear. Neither do memories of any other type.

## Rules

### Header

- **Line 1** is the slug. When the project has a name that is neither empty nor equal to the slug,
  it is followed by ` — ` and the name.
- **Line 2** is the status, translated when it is `active`, `paused` or `done` and shown as written
  otherwise, followed by ` · <updated label> <date>` when the project has one.
- Services are not on the card. They describe the project, not the work left in it.

### Tasks

- **The header** reads `<TODO>  <n> <open>`, counting every open task from the three sources.
  When tasks belong to another machine it adds ` · <k> <on> <machine>`, or ` · <k> <elsewhere>` when
  they belong to more than one other machine.
- **Five tasks are shown**, numbered from 1. Past that, a line `  +<n> <more>`.
- **An item's text** is truncated to 68 code points. A `[@machine]` stamp is removed from the text
  and shown in its own column.
- **The columns** follow the text, each after two spaces: first the origin, when the task has one,
  then the machine, when the task belongs to another machine.
- **With no open tasks**, the block is one line: `<TODO>  <none>`.

## Words

| Key | en | es |
|---|---|---|
| TODO | `TODO` | `PENDIENTES` |
| open | `open` | `abiertas` |
| on | `on` | `en` |
| elsewhere | `elsewhere` | `en otras máquinas` |
| more | `more` | `más` |
| none | `none` | `ninguno` |
| updated | `updated` | `actualizado` |
| note | `note` | `nota` |
| active / paused / done | `active` / `paused` / `done` | `activo` / `pausado` / `hecho` |

A section's label is shown as it is written in the file, lower-cased, whatever the language of the
card.

## Examples

A project with work in flight, in English, loaded from `fixture-box`:

```
busy-api — Busy API
active · updated 2026-08-10

TODO  11 open · 1 on other-box
  1  Finish the token rotation rollout
  2  Migrate 🐛 the legacy exporter the cursor pagination rewrite touches every list…
  3  Backfill the audit table  other-box
  4  Document the cursor contract
  5  Drop the v1 rate limiter
  +6 more
```

The emoji is part of what the user wrote in that task. mnemo adds none of its own.

A finished project with nothing left, in Spanish:

```
finished-app — Finished App
hecho · actualizado 2026-01-20

PENDIENTES  ninguno
```

## Tests

- **Expected files** in `testdata/cards/<slug>.<lang>.<machine>.txt` hold the render for every
  fixture project, in both languages, from a machine that matches the fixture stamps and one that
  does not. The test compares the render against them byte for byte.
- **They are written by hand, from this document**, and reviewed when they change. A test that
  regenerates its own expectation proves nothing.
- **A change to the layout** shows up as a diff in every expected file, which is the point: the
  card's shape is not something to change by accident.
