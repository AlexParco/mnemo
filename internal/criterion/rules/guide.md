# mnemo — persistent project memory

The mnemo MCP server holds this user's memory across sessions and machines: plain-text notes in a git store.
The tools store and retrieve; they cannot see the conversation, so the judgement below is yours.

## Starting work

Load the project's memory with `mnemo_load_project` before working on something the user has worked on before,
and print the card it returns verbatim, without prose around it. Slugs are exact — call `mnemo_list_projects`
rather than guessing one. Before answering "what did we decide about X", search instead of reconstructing.

## Saving

{{SAVE_CRITERION}}

{{PENDING_CRITERION}}

Write each fact with `mnemo_write_memory`, one fact per call, and the project's living state with
`mnemo_write_pending`. Read an existing note with `mnemo_read_memory` before replacing it. A project the user has
confirmed is created with `mnemo_upsert_project`: a write to a slug that does not exist is refused, never created.

Writes are not committed as they happen. Write what the session produced, then call `mnemo_commit` once, so one
session is one commit. Pushing is separate again: until `mnemo_push` runs, the memory is only on this machine.

## Machines

{{MACHINE_RULE}}

## Syncing

Call `mnemo_sync` before writing, so a save does not land on top of a stale version.

{{CONFLICT_RULE}}

## Confirmation

{{CONFIRMATION_RULE}}

## Mailbox

{{MAILBOX_RULE}}
