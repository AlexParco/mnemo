---
name: load-context
description: Load a project/initiative's persistent memory at the start of a Claude session — decisions, constraints, gotchas and pending tasks — to resume work where it was left off. Usage/Uso "/mnemo:load-context <project>". Triggers: user asks to load a project's context, resume a project, start a new session to continue a prior initiative, plus original Spanish phrases "carga el contexto de X", "seguir con X", "en qué quedamos en X". Works for any project / sirve para cualquier proyecto.
---

# load-context

Retrieves a project's memory and leaves the assistant ready to continue the work.

The store, the git sync and the card render belong to the **mnemo MCP server**; this skill is about
what to do with what comes back. If the `mnemo_*` tools are not available, the server is not
running — say so and point at the plugin README rather than improvising with shell commands.

**Output language:** write all user-facing output in the language the user is writing in (Spanish
or English).

## Steps

0. **The slug is required.** If the user did not name a project, do not load anything: call
   `mnemo_list_projects` and ask which one they mean. Never guess, and never load "the last one".

1. **Sync.** Call `mnemo_sync` first, so you resume from what the other machines saved rather than
   from a stale copy. If it reports conflicts, stop and tell the user: resolving them belongs to
   `/mnemo:save-context`, which merges, not here.

2. **Load** with `mnemo_load_project`, passing the slug and `lang` — `es` if the user is writing in
   Spanish, otherwise `en`. If the slug does not exist the tool answers with the real ones: ask
   which they meant, do not invent a project.

3. **Show the card, and only the card.** The tool's **first content block** is the rendered resume
   card. Print it **verbatim as the entire response**, with no prose before or after. The blocks
   after it are for you: keep them in context, and unfold the relevant part only if the user asks
   for the detail, the decisions, or what there was about some topic.

4. **Do not load other projects.** Only the one asked for. A memory shared with another project
   comes along by overlap — use it, but do not drag in the rest of that other project.

## Machine awareness

<!-- mnemo:rule MACHINE_RULE -->
**Machine-bound work.** Memory is shared across the user's machines, but some pending items belong to one only.
Those are stamped `[@<machine>]` and the card marks them ⚠ when they are not from here.

Do not act on them here: do not look for their repo, do not commit or push them. Say the work belongs to that
machine instead. Before any git or build action on a code repo, check it exists on this machine.

When saving, stamp `[@<machine>]` ONLY on what is physically tied to one machine: uncommitted changes, a local
unpushed branch, a service running there, a local path. Test: could any machine that has the repo do it? Then it
is portable — do not stamp it. When unsure, do not stamp: a portable item wrongly marked ⚠ is as confusing as a
local one left unmarked.
<!-- /mnemo:rule -->

## Notes

- Read-only. This skill never writes. To save, that is `/mnemo:save-context`.
- No slug means list mode (step 0), never a default load.
