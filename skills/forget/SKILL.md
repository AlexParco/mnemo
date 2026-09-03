---
name: forget
description: Delete a whole project or a single loose memory from persistent memory, safely handling overlap (a memory shared with other projects gets untagged, not deleted). Usage/Uso "/mnemo:forget project <slug>" or "/mnemo:forget memory <id>". Triggers when the user wants to delete/remove a project from memory or clean up store entries, "delete project X", "forget X", "remove X from memory", plus the Spanish phrases "borra el proyecto X", "olvida X", "elimina X de la memoria". Works for any project / sirve para cualquier proyecto.
---

# forget

Delete entries from the store. **Destructive** — the `mnemo_forget` tool enforces the two phases,
but only you can make sure a human agreed in between.

If the `mnemo_*` tools are not available, the MCP server is not running — say so and stop.

**Output language:** write all user-facing output in the language the user is writing in (Spanish
or English).

## Steps

1. **Sync** with `mnemo_sync`, so you are not deleting against an old copy: a memory another
   machine just wrote should be in front of you before you decide.

2. **Ask for the plan.** Call `mnemo_forget` with `kind` (`project` or `memory`) and `target`, and
   **no `confirm`**. Nothing is deleted; you get back exactly what would go.

3. **Show the user that plan in full**: the project directory, the memories that would be deleted,
   the shared ones that would be untagged and which projects they keep, and any `[[id]]` links that
   would be left dangling.

4. **Wait for an explicit yes.** Then call `mnemo_forget` again with the same arguments plus
   `confirm` set to the value the plan returned.

5. Report what happened, and that the other machines keep what was deleted until it is pushed.

## Rules

<!-- mnemo:rule CONFIRM_NOTE -->
Show the user exactly what this would change and wait for an explicit yes. Only then call this again with `confirm` set to the value above. Never confirm on your own judgement — git is the only undo there is.
<!-- /mnemo:rule -->

- **A shared memory is never deleted** when one of its projects goes: it is untagged and survives.
  The tool guarantees this; the plan tells the user it will happen.
- **On ambiguity, ask.** "Delete X" without saying whether X is a project or a memory is not enough
  to act on. A slug that does not exist, or a dubious id, is not either.
- Dangling `[[id]]` links are reported, never repaired silently. Mention them; let the user decide.
