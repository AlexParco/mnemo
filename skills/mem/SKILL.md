---
name: mem
description: Save a loose note into persistent memory mid-session, tagged by project, without closing or syncing everything. Usage/Uso: "/mnemo:mem <project>[,project2] <the note>". Triggers: user wants to quickly jot down a decision, gotcha or fact into one or several projects' memory, "note that...", "remember for X that...", without doing a full save-context, plus "apunta que...", "recuerda para X que...". Works for any project / sirve para cualquier proyecto.
---

# mem

Shortcut to capture a single atomic memory without the full `/mnemo:save-context` flow.

**Output language:** write all user-facing output in the language the user is writing in (Spanish or English).

## Store

`$MEM = $MNEMO_DIR` or `~/.local/share/mnemo`.

**If `$MEM` doesn't exist or isn't a git repo:** with `MNEMO_REMOTE` set, clone it from the hub
(`git clone "$MNEMO_REMOTE" "$MEM"`) to work on the shared memory; without `MNEMO_REMOTE`,
or if the project doesn't exist yet, `/mnemo:mem` doesn't create the store: suggest `/mnemo:save-context <slug>`
and stop.

## Steps

0. **Sync** if there's a remote: `git -C $MEM pull --rebase --autostash`. If it conflicts, merge
   keeping both sides (or stop and ask if the clash is a semantic conflict) before writing, and
   continue with `GIT_EDITOR=true git -C $MEM rebase --continue` — without `GIT_EDITOR` the shell
   hangs in the editor.

1. **Parse the argument.** First token = project(s), comma-separated for overlap
   (`rappi-f3,inventory-hotfix`). The rest = the note content. If there's no clear project,
   ask for it (offer the existing projects from `$MEM/projects`).

2. **Verify the projects exist** in `$MEM/projects/`. If any doesn't exist, flag it and
   ask whether to create it or fix the slug (don't create it silently).

3. **Write a memory** to `$MEM/memories/<id>.md` per `shared/SCHEMA.md`:
   - `id` kebab-case derived from the content, unique (check it doesn't exist; if the topic already exists,
     update that memory).
   - `projects: [...]` with all the given slugs (overlap).
   - Infer `type` and `services` from the content; `updated` (today) and `author`
     (`git -C $MEM config user.name`).

4. **Local commit** `git -C $MEM add -A && git -C $MEM commit -m "mem(<slugs>): <summary>"`.
   No `Co-Authored-By`.
   - **By default don't push** — the push happens in `/mnemo:save-context` (or if the user asks).
   - **With `MNEMO_AUTOPUSH=1`**, push just like save-context: first the **secret scan**
     (grep the `origin/main..HEAD` diff for keys/tokens/connection strings; if there's a match, don't
     push and warn), and if it's clean, `git -C $MEM push` and report `pushed ✓`.

5. Confirm in one line what was saved and where.
