---
name: mem
description: Save a loose note into persistent memory mid-session, tagged by project, without closing or syncing everything. Usage/Uso: "/mnemo:mem <project>[,project2] <the note>". Triggers: user wants to quickly jot down a decision, gotcha or fact into one or several projects' memory, "note that...", "remember for X that...", without doing a full save-context, plus "apunta que...", "recuerda para X que...". Works for any project / sirve para cualquier proyecto.
---

# mem

Capture a single fact without the full `/mnemo:save-context` flow.

If the `mnemo_*` tools are not available, the MCP server is not running — say so.

**Output language:** write all user-facing output in the language the user is writing in (Spanish
or English).

## Steps

1. **Parse the argument.** First token = project slug(s), comma-separated for genuine overlap
   (`rappi-f3,inventory-hotfix`). The rest is the note. With no clear project, ask — offer what
   `mnemo_list_projects` returns.

2. **Sync** with `mnemo_sync`, so the note does not land on a stale copy.

3. **Search before writing.** `mnemo_search_memories` on the topic. If the fact already has a note,
   update that one (`mnemo_write_memory` with `overwrite: true`, after reading and merging it)
   instead of adding a near-duplicate.

4. **Write it** with `mnemo_write_memory`: a kebab-case `id` derived from the content, the given
   slugs in `projects`, and `type`/`services` inferred from what the note says.

5. **Commit** with `mnemo_commit`, message `mem(<slugs>): <summary>`.

6. **Push** only per the rule below, then confirm in one line what was saved and where.

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

## Pushing

`mnemo_commit` leaves the note on this machine. Call `mnemo_status`: if it reports `autopush: on`,
call `mnemo_push` without asking. Otherwise ask the user first, and if they decline, say the note
is committed locally and the other machines will not see it until it is pushed.
