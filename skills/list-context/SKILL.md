---
name: list-context
description: Show the overview of the memory store — every project with its status, services and how many memories each one has. Usage/Uso: "/mnemo:list-context". Triggers: user wants to see which projects exist in memory, list projects, "what projects do I have", "show me the memory", "which projects am I working on", or doesn't remember a project's exact slug, plus "qué proyectos existen en la memoria", "listar proyectos", "qué proyectos tengo", "muéstrame la memoria", "en qué proyectos estoy trabajando". Works for any project / sirve para cualquier proyecto.
---

# list-context

Gives a bird's-eye view of the `mnemo` store: which projects exist and their size.

**Output language:** write all user-facing output in the language the user is writing in (Spanish or English).

## Store

`$MEM = $MNEMO_DIR` or `~/.local/share/mnemo`.

**If `$MEM` doesn't exist or isn't a git repo, adopt the shared store first:**
- `MNEMO_REMOTE` set → clone it from the hub: `git clone "$MNEMO_REMOTE" "$MEM"`. Brings all the
  memory from your other machines (works for SSH remote or a local path). Then continue normally.
- No `MNEMO_REMOTE` → there's no memory yet: suggest `/mnemo:save-context <slug>` and stop.

## Steps

1. **Sync** if there's a remote (`git -C $MEM remote`): `git -C $MEM pull --rebase --autostash`.
   If it fails or the rebase conflicts, don't resolve it here (this is read-only): mention it in the
   summary and continue with the local copy.

2. **Gather the projects** from `$MEM/projects/*/INDEX.md`. For each one, read from the frontmatter:
   `slug`, `name`, `status`, `services`, `updated`.

3. **Count memories per project**: for each slug, how many memories in `$MEM/memories/` have it
   in their `projects` (use `grep -rl` as a filter and refine by reading the frontmatter). Mark
   how many of those are **shared** (tagged with more than one project).

4. **Present a table** ordered by `status` (active first) and then by `updated` desc:

   | project (slug) | status | memories | services | updated |
   |---|---|---|---|---|

   If a memory is shared, reflect it (e.g. "8 (3 shared)").

5. **Final summary** in one line: total projects, total memories, how many live in
   `shared/`. If the store is empty (no projects), say so and remind that the first one is created
   with `/mnemo:save-context <slug>`.

## Notes

- **Compact output:** only the table and the summary line. No surrounding prose and no
  describing each project — for the detail of one, use `/mnemo:load-context <slug>`.
- Read-only. Doesn't write or commit.
- Don't load the content of any project; this is just the index.
