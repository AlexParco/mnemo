---
name: rename
description: Rename the slug (the identity) of a project in persistent memory, updating the directory, the INDEX and the projects field of each tagged memory, safely handling overlap. Usage/Uso "/mnemo:rename <old-slug> <new-slug>". Triggers when the user wants to rename a project or change its slug/identifier, "rename project X to Y", "change the slug of X", plus the Spanish phrases "renombra el proyecto X a Y", "cambia el slug de X". To change only the readable name (not the slug) just edit the INDEX. Works for any project / sirve para cualquier proyecto.
---

# rename

Change the **slug** of a project (its identity) across the whole store. The slug lives in the
directory name, in `INDEX.md`, and in the `projects` field of each tagged memory — you have to touch all
three or the project ends up inconsistent. **Mutating** operation → confirmation protocol.

> Just want to change the **readable name** (the `name:` that is shown, not the slug)? That is a
> one-line edit in `INDEX.md`, it does not need this skill. `rename` is for the **slug**.

**Output language:** write all user-facing output in the language the user is writing in (Spanish or English).

## Store

`$MEM = $MNEMO_DIR` or `~/.local/share/mnemo`.

**If `$MEM` does not exist or is not a git repo:** with `MNEMO_REMOTE` set, clone it from the hub
(`git clone "$MNEMO_REMOTE" "$MEM"`) to rename against the real shared memory; without
`MNEMO_REMOTE`, there is nothing to rename: warn and stop.

## Sync first

If there is a remote, `git -C $MEM pull --rebase --autostash` before touching anything, so as not to rename against
an old copy. If it conflicts, resolve it (or stop and ask if the clash is a semantic conflict) and continue
with `GIT_EDITOR=true git -C $MEM rebase --continue` — without `GIT_EDITOR` the shell hangs in the editor.

## Steps

1. **Parse the argument:** `<old-slug> <new-slug>`. If either is missing, ask for it. The new slug
   must be valid kebab-case.

2. **Validate:**
   - `$MEM/projects/<old-slug>/` **exists**. If not, list the projects (`ls $MEM/projects`) and
     ask which one they meant.
   - `$MEM/projects/<new-slug>/` **does NOT exist**. If it exists, stop: renaming would merge two
     projects and this skill does not do merges. Warn the user.

3. **Gather the impact and confirm it.** Find the memories that tag the old slug:
   `grep -rl "<old-slug>" $MEM/memories`. **Read the `projects` field of each one** to keep
   only those that list it as a project (not those that mention it in prose; a short slug
   can appear inside another, e.g. `mnemo` inside `mnemo-web`). **Show the user** what will
   change (directory, INDEX, and the list of affected memories) and **ask for explicit
   confirmation** before touching anything.

4. **Execute:**
   - Rename the directory: `git -C $MEM mv projects/<old-slug> projects/<new-slug>`.
   - In `$MEM/projects/<new-slug>/INDEX.md`, change `slug: <old-slug>` → `slug: <new-slug>`.
   - In **each affected memory**, change the slug **only inside the `projects:` field** of the
     frontmatter (respect the overlap: if it is `projects: [<old-slug>, other]`, it becomes
     `projects: [<new-slug>, other]`). Do not touch the body prose.
   - Update `updated` (today) in the `INDEX.md` and in the memories you edited.

5. **Verify afterward:** no frontmatter should list `<old-slug>` as a project anymore (check the
   `projects` field of the memories you grepped, not the bare slug against the whole store),
   `projects/<old-slug>/` no longer exists and `projects/<new-slug>/` does. Report the result.

6. **Commit** `git -C $MEM add -A && git -C $MEM commit -m "rename(<old-slug> → <new-slug>): <summary>"`.
   No `Co-Authored-By`. **Push only with separate confirmation** (show what will be uploaded). Remind them
   that until they push, the other machines keep the old slug.

## Notes

- The new slug is the new identity: from here on `/mnemo:load-context <new-slug>`.
- If the project had a `pending.md` with the name in the title, update it too if applicable.
