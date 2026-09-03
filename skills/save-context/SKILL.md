---
name: save-context
description: Saves the current session's progress, decisions, and pending work to persistent memory, tagged by project, and syncs it to the mnemo git store. Usage/Uso "/mnemo:save-context <project>". Triggers when the user wants to save context, close/pause a session while preserving progress, "save what we did in X", "note this in the memory of X", or before switching projects, plus the original Spanish phrases "guarda lo que hicimos en X", "apunta esto en la memoria de X". Works for any project / sirve para cualquier proyecto.
---

# save-context

Persists what the session learned as tagged atomic memories, updates the project's pending list,
and commits it.

The store, git and the schema belong to the **mnemo MCP server**. What belongs to you is the part
it cannot do: deciding what is worth keeping. The server cannot see this conversation. If the
`mnemo_*` tools are not available, the server is not running — say so and stop, rather than writing
files by hand.

**Output language:** write all user-facing output in the language the user is writing in (Spanish
or English). These instructions are in English; your reply follows THEIR language.

## Steps

0. **The slug is required.** With no project named, save nothing: call `mnemo_list_projects` and ask
   which one, or ask them to confirm a new slug. Never save to a default and never guess.

1. **Sync first.** `mnemo_sync`, before writing anything, so you do not save on top of a stale
   version. If it reports conflicts, resolve them now — see *Conflicts* below.

2. **Resolve the project.** If the slug does not exist, offer to create it: ask for a readable name
   and call `mnemo_upsert_project`. Never create a project silently.

3. **Distil the session.** Review the work and extract only what is worth persisting, per the
   criterion below. Write each fact with `mnemo_write_memory`, searching first with
   `mnemo_search_memories` so an existing note is updated rather than duplicated. **If there is
   nothing worth keeping, say so and write nothing** — do not manufacture a note to have saved
   something.

4. **Update pending** with `mnemo_write_pending`. It replaces the whole file, so build the new
   version from what `mnemo_load_project` returned plus what this session changed.

5. **Commit once** with `mnemo_commit`, message `save(<slug>): <short summary>`. One session, one
   commit.

6. **Push** per the rule below.

## What belongs in a memory

<!-- mnemo:rule SAVE_CRITERION -->
**What to persist.** Decisions made, constraints that must not be broken, gotchas, known bugs, useful references.
Not what is already in the code, in git history, or ephemeral to this conversation. If there is nothing worth
keeping, say so — do not invent a note to have saved something.

**One fact per file.** If a note mixes two topics, write two. Search first with mnemo_search_memories and update
the existing note instead of adding a near-duplicate.

**`projects` vs `services`.** `projects` means genuinely different projects that share the same fact — that is the
overlap, and it is rare. Parts of ONE project (repos, modules, areas) go in `services`. Two repos of the same
product are one project with two services, not two projects. Ask the user before tagging a second project.
<!-- /mnemo:rule -->

## What belongs in pending

<!-- mnemo:rule PENDING_CRITERION -->
**Pending is the project's living state**, what a later session resumes from. `mnemo_write_pending` replaces the
whole file, so load the project first and send the merged result.

Sections are free-form. `## In progress` and `## Next` feed the card's numbered list; anything else you add
(`## Blocked`, `## Debt`, `## Branches`, `## Deployed`, `## Risks`) renders as its own block. Add only the ones
this project needs; do not copy another project's or force empty ones.
<!-- /mnemo:rule -->

## Machine-bound items

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

## Pushing

Call `mnemo_status`: if it reports `autopush: on`, call `mnemo_push` without asking. Otherwise show
what would go and ask first; if the user declines, leave it committed and say the other machines
will not see it until it is pushed.

`mnemo_push` scans for secrets before every push and there is no way around that. If it refuses,
**show the user the findings** — they name a file and a line with the match redacted — and pass the
acknowledgement back only if the user confirms it is a false positive.

## Conflicts

<!-- mnemo:rule CONFLICT_RULE -->
**Merging a conflict.** Keep the information from BOTH sides — losing a memory is worse than a redundant note.
For pending.md the right merge is almost always the union of the tasks, minus duplicates, respecting anything
already marked done on either side.

If the two sides assert contradictory things, STOP and ask the user which one holds. Do not decide that yourself.
<!-- /mnemo:rule -->

Write each merged file back with `mnemo_resolve_conflict`, then finish with `mnemo_rebase` action
`continue`. If the merge is going wrong, `mnemo_rebase` action `abort` restores the store exactly
as it was — say so rather than leaving it half-merged.

## Output format

**Compact** confirmation, not a dump of what you wrote:

```
✅ Saved to <slug>
 • Memories: <N> new/updated — <ids or short topics>
 • Pending: <what changed>
 • Commit <short hash> · <"pushed to hub ✓" | "local, not pushed">
```

Do not repeat each memory's content back at the user; they lived the session. If there was nothing
to save, one line: "Nothing new to persist in <slug>."
