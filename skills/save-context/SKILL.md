---
name: save-context
description: Saves the current session's progress, decisions, and pending work to persistent memory, tagged by project, and syncs it to the mnemo git store. Usage/Uso "/mnemo:save-context <project>". Triggers when the user wants to save context, close/pause a session while preserving progress, "save what we did in X", "note this in the memory of X", or before switching projects, plus the original Spanish phrases "guarda lo que hicimos en X", "apunta esto en la memoria de X". Works for any project / sirve para cualquier proyecto.
---

# save-context

Persists what was learned/decided in the session as tagged atomic memories and updates the
project's Pending, syncing to the git repo.

**Output language:** write all user-facing output in the language the user is writing in (Spanish or
English). These instructions are in English; your reply to the user follows THEIR language.

## Store resolution

`$MEM = $MNEMO_DIR` or `~/.local/share/mnemo`.

**Bootstrap (first use).** `save-context` is the command that provisions the store: if `$MEM` does
not exist or is not a git repo, create it before continuing. Run:

```bash
mkdir -p "$MEM"/{projects,memories,shared}
git -C "$MEM" rev-parse --git-dir >/dev/null 2>&1 || git -C "$MEM" init -q -b main
# Optional remote + history adoption. Goes BEFORE writing local templates:
# on a new machine against a VPS that already has memory, the checkout would bring
# the store, and an unversioned .gitignore/SCHEMA.md would block it.
if [ -n "${MNEMO_REMOTE:-}" ] && ! git -C "$MEM" remote get-url origin >/dev/null 2>&1; then
  git -C "$MEM" remote add origin "$MNEMO_REMOTE"
  if git -C "$MEM" fetch -q origin 2>/dev/null && git -C "$MEM" rev-parse -q --verify origin/main >/dev/null; then
    git -C "$MEM" rev-parse -q --verify HEAD >/dev/null || git -C "$MEM" checkout -q -B main --track origin/main
  fi
fi
# Templates: only what's missing (on a new machine they already came from the remote).
[ -f "$MEM/.gitignore" ] || printf '.DS_Store\n' > "$MEM/.gitignore"
# copy the schema contract if the plugin has it handy (best-effort)
[ -f "$MEM/shared/SCHEMA.md" ] || cp "${CLAUDE_PLUGIN_ROOT:-/nonexistent}/templates/SCHEMA.md" "$MEM/shared/SCHEMA.md" 2>/dev/null || true
```

If the bootstrap just created the store, tell the user in one line where it landed (`$MEM`) and
that to sync across machines they can export `MNEMO_REMOTE` or add a git remote by hand.

## Steps

0. **The slug is required.** If the user did not pass a project, **do not save anything**: run the
   behavior of `/mnemo:list-context` (show the existing projects) and ask which one to save to, or
   to confirm a new slug. Never save to a "default" or guess the project.

1. **Sync first.** If there is a remote, `git -C $MEM pull --rebase --autostash` **before writing
   anything**, so you don't save on top of a stale version. If it conflicts, resolve it now (see
   "Conflicts" below). If there is no remote, continue locally.

2. **Resolve the project** (slug argument). If `$MEM/projects/<slug>/` does not exist, offer to
   create it: ask for a readable name and create `INDEX.md` (with SCHEMA frontmatter) and an empty
   `pending.md`. Do not create a project silently.

3. **Distill the session.** Review the work done and extract ONLY what is worth persisting:
   decisions made, constraints/invariants discovered, gotchas, known bugs, useful references. Do
   **not** save what is already in the code, in git, or is ephemeral to this conversation. When in
   doubt, less is more. **If there is nothing worthwhile, tell the user and do not create empty or
   trivial notes just to "save something".**

4. **Write atomic memories** in `$MEM/memories/<id>.md` following `shared/SCHEMA.md`:
   - One fact per file. `id` = file name, unique kebab-case.
   - `projects: [<slug>, ...]` — normally just the current slug. **Overlap** = add ANOTHER project
     only if the same fact also serves a **different** project (e.g. a bug in a shared library). Do
     not confuse overlap with internal parts of the same project: to distinguish repos/modules/areas
     of this project use `services`, **not** `projects`. Ask the user before tagging a second project
     if it isn't clear.
   - Fill in `services` (which repo/module/area it touches within the project), `type`, `updated`
     (today's date) and `author` (`git -C $MEM config user.name`).
   - **Before creating, search for a duplicate** (`grep` by topic/id). If it exists, update that
     file and its `updated` instead of duplicating.
   - Link related memories with `[[id]]`.

5. **Update Pending** `$MEM/projects/<slug>/pending.md`: check off what's done, add what's new,
   make clear what is still in progress for the next `/mnemo:load-context`. The sections are **free**
   and specific to each project (`load-context` shows whichever exist): besides
   `In progress`/`Next`/`Blocked`, add whatever the session produced that helps resume — e.g.
   `## Debt`, `## Branches` (what was pushed to which branch), `## Done`/`Deployed` (`- [x]`, so you
   don't redo it), `## Risks`. **Only the ones that apply to THIS project**; don't force empty
   sections or copy another project's. Update `updated` and status info in `INDEX.md` if it changed.
   - **Stamp `[@<machine>]` ONLY on items physically bound to ONE machine** (`<machine>` =
     `${MNEMO_MACHINE:-$(hostname -s)}`): uncommitted changes, a process/service running here, a
     local unpushed branch, a local path. **Most Pending items are NOT stamped.**
     **Test:** *could any machine that has the repo do it?* → **yes = portable, do NOT stamp**
     ("implement X", "the front should…", design decisions hold anywhere). Stamp only what **does
     not exist** on another machine. When in doubt, **don't stamp**: a portable item marked ⚠ by
     mistake is as confusing as a local one left unmarked.

6. **Commit.** `git -C $MEM add -A && git -C $MEM commit -m "save(<slug>): <short summary>"`.
   Never use `Co-Authored-By`.

7. **Push.** If there is a remote:

   a. **Secret scan — ALWAYS, not optional.** Before any push, scan what would be uploaded for
      secret patterns:
      ```bash
      git -C $MEM diff origin/main..HEAD 2>/dev/null | grep -inE -e \
        '-----BEGIN [A-Z ]*PRIVATE KEY-----|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-|(password|passwd|secret|token|api[_-]?key)["'"'"' ]*[:=]|[a-z][a-z0-9+.-]*://[^/:@ ]+:[^/@ ]+@'
      ```
      (The `-e` is necessary: the pattern starts with `---` and without `-e` grep treats it as flags.)

      If there's a match → **do NOT push**, show the user the lines and stop. Memory carries no
      secrets; this catches them before they leave the machine. (If `origin/main` didn't resolve,
      scan the new commit(s) with `git -C $MEM show HEAD`.)

   b. **Push mode:**
      - **`MNEMO_AUTOPUSH` set (1/true)** → push automatically (`git -C $MEM push`) and report
        `pushed to hub ✓`. No friction.
      - **Without `MNEMO_AUTOPUSH`** (default) → show what will be uploaded
        (`git -C $MEM log origin/main..HEAD --oneline` and `git -C $MEM show --stat HEAD`) and **ask
        for explicit confirmation** before `git push`. Without confirmation, leave the commit local
        and note that it wasn't pushed.

   A **semantic** conflict in the rebase (step 1) already stopped you before reaching here — the
   auto-push only runs over a clean merge.

## Conflicts

With a shared remote, two machines can touch the same note. The store is `.md`, so the rebase is
resolved by reading, not guessing:

- **Merge, don't discard.** Faced with conflict markers, the default is to **keep the information
  from both sides**. Losing a memory is worse than leaving a redundant note.
- **`pending.md` is the only hot file** (memories are one fact per file, they almost never clash).
  The correct merge is almost always the **union** of the tasks from both sides; remove duplicates
  and respect whatever is already marked done on either.
- **If the clash is semantic —the two sides assert contradictory things— stop and ask.** Don't
  decide yourself which decision survives.
- Once the file is resolved: `git -C $MEM add <archivo>` and then
  **`GIT_EDITOR=true git -C $MEM rebase --continue`**. The `GIT_EDITOR=true` is not optional:
  without it git opens an editor for the commit message and your shell hangs waiting. (`--continue`
  doesn't accept `-q` either.)
- If you get tangled, `git -C $MEM rebase --abort` leaves everything as it was and you tell the user.
- Before considering it closed: `git -C $MEM status` must not show a rebase in progress, and no
  `<<<<<<<` may remain in the `.md` (`grep -rn '^<<<<<<<' $MEM`).

## Output format

**Compact** confirmation, not a dump of what you wrote. Format:

```
✅ Saved to <slug>
 • Memories: <N> new/updated — <ids or short topics, comma-separated>
 • Pending: <what changed, e.g. "+2 new, 1 marked done">
 • Commit <short hash> · <"pushed to hub ✓" | "local, not pushed (confirm to push)">
```

Don't repeat the content of each memory; the user already lived the session. If there was nothing
to save, one line: "Nothing new to persist in <slug>."

## Notes

- Push: by default it's confirmed separately (invoking `/mnemo:save-context` authorizes the local
  commit, not the push). With `MNEMO_AUTOPUSH=1` the push is automatic — but the **secret scan and
  the stop on semantic conflict run all the same**, they are never skipped.
