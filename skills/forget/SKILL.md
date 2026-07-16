---
name: forget
description: Delete a whole project or a single loose memory from persistent memory, safely handling overlap (a memory shared with other projects gets untagged, not deleted). Usage/Uso "/mnemo:forget project <slug>" or "/mnemo:forget memory <id>". Triggers when the user wants to delete/remove a project from memory or clean up store entries, "delete project X", "forget X", "remove X from memory", plus the Spanish phrases "borra el proyecto X", "olvida X", "elimina X de la memoria". Works for any project / sirve para cualquier proyecto.
---

# forget

Delete entries from the `mnemo` store. **Destructive** operation → mandatory
confirmation protocol. Never delete without first showing what will go and without explicit confirmation.

**Output language:** write all user-facing output in the language the user is writing in (Spanish or English).

## Store

`$MEM = $MNEMO_DIR` or `~/.local/share/mnemo`.

**If `$MEM` does not exist or is not a git repo:** with `MNEMO_REMOTE` set, clone it from the hub
(`git clone "$MNEMO_REMOTE" "$MEM"`) to delete against the real shared memory; without
`MNEMO_REMOTE`, there is nothing to delete: warn and stop.

## Sync first

If there is a remote, `git -C $MEM pull --rebase --autostash` before touching anything: deleting against an
old copy can wipe a memory another machine just wrote. If the pull conflicts,
resolve it (or stop and ask) **before** classifying anything, and continue with
`GIT_EDITOR=true git -C $MEM rebase --continue` — without `GIT_EDITOR` the shell hangs in the editor.

## Mode A — delete a project: `/mnemo:forget project <slug>`

1. **Verify** that `$MEM/projects/<slug>/` exists. If not, say so and list the projects that do
   exist. Do not assume.

2. **Classify the impact** BEFORE deleting. Walk `$MEM/memories/` and split the memories whose
   frontmatter `projects` includes `<slug>` into two groups:
   - **Exclusive** (`projects` == `[<slug>]`, only that one) → will be DELETED.
   - **Shared** (`projects` has `<slug>` + others) → will be UNTAGGED (remove `<slug>`
     from the list, the memory survives for the other projects).

3. **Show the exact plan** to the user and ask for confirmation:
   - `projects/<slug>/` (INDEX.md, pending.md) → deleted.
   - List of exclusive memories to delete (by `id`).
   - List of shared memories to untag, indicating which other projects they stay with.
   - **Wait for an explicit "yes".** Without confirmation, delete nothing.

4. **Execute:**
   - Remove `<slug>` from the `projects` of each shared memory (leave the rest intact) and update
     its `updated`.
   - Delete the files of the exclusive memories.
   - Delete the `projects/<slug>/` directory.

5. **Verify afterward:** confirm that no frontmatter still lists `<slug>` as a project, and
   that the shared memories still exist with their other projects. To verify, **read the
   `projects` field of the memories you grepped in step 2**; do not grep the bare slug against
   the whole store: it appears in note prose and will give you false positives (and a short slug like
   `mnemo` matches inside `mnemo-web`). Report the result.

6. **Commit** `git -C $MEM add -A && git -C $MEM commit -m "forget(project <slug>): <summary>"`.
   No `Co-Authored-By`. **Push only with separate confirmation** (show what will be uploaded). Remind
   the user that until they push, the other machines keep what was deleted.

## Mode B — delete a memory: `/mnemo:forget memory <id>`

1. Verify that `$MEM/memories/<id>.md` exists. If not, say so (offer to search by topic).
2. Show its content and which projects it is tagged with; ask for explicit confirmation.
3. On confirmation, delete the file. If any `pending.md` or memory linked to it with `[[id]]`,
   warn about those broken links (do not fix them silently).
4. Verify it no longer exists. Commit with message `forget(memory <id>)`. Push only confirmed.

## Rules

- On ambiguity (slug does not exist, dubious id, "delete X" without saying whether it is a project or a memory),
  **ask**; do not guess what to delete.
- A shared memory is NEVER deleted when deleting just one of its projects: it is untagged.
- Respect the global rule: no push without explicit confirmation from the user.
