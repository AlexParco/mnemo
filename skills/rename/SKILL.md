---
name: rename
description: Rename the slug (the identity) of a project in persistent memory, updating the directory, the INDEX and the projects field of each tagged memory, safely handling overlap. Usage/Uso "/mnemo:rename <old-slug> <new-slug>". Triggers when the user wants to rename a project or change its slug/identifier, "rename project X to Y", "change the slug of X", plus the Spanish phrases "renombra el proyecto X a Y", "cambia el slug de X". To change only the readable name (not the slug) just edit the INDEX. Works for any project / sirve para cualquier proyecto.
---

# rename

Change a project's **slug** — its identity — across the whole store: the directory, `INDEX.md`, and
the `projects` field of every memory tagged with it.

> Only want to change the **readable name**? That is `mnemo_upsert_project` with a new `name`, not
> this. `rename` is for the slug.

If the `mnemo_*` tools are not available, the MCP server is not running — say so and stop.

**Output language:** write all user-facing output in the language the user is writing in (Spanish
or English).

## Steps

1. **Parse** `<old-slug> <new-slug>`. If either is missing, ask.

2. **Sync** with `mnemo_sync`, so you are not renaming against an old copy.

3. **Ask for the plan.** Call `mnemo_rename` with `from` and `to` and **no `confirm`**. Nothing
   moves; you get back the memories that would be rewritten. The tool refuses a slug that already
   exists — that would be a merge, which it does not do — and a slug that is not kebab-case.

4. **Show the user what would change** and **wait for an explicit yes**, then call again with
   `confirm` set to the value the plan returned.

5. Report the result, that the project now loads under the new slug, and that the other machines
   keep the old one until it is pushed.

## Rules

<!-- mnemo:rule CONFIRM_NOTE -->
Show the user exactly what this would change and wait for an explicit yes. Only then call this again with `confirm` set to the value above. Never confirm on your own judgement — git is the only undo there is.
<!-- /mnemo:rule -->

- Renaming preserves overlap: a memory tagged `[old, other]` becomes `[new, other]`, and prose in
  the body that happens to mention the old slug is left alone.
