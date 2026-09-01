/** The behaviour layer: the rules a model needs that no tool can enforce.
 *
 * This module is the single source for them. They reach a host through three
 * channels, and which channels exist depends on the client:
 *
 *   1. the server's `instructions` field — part of the base protocol, and the
 *      only one that reaches Codex, which supports neither prompts nor resources;
 *   2. MCP prompts — Claude Code, Cursor and VS Code surface them;
 *   3. a snippet the user pastes into AGENTS.md / CLAUDE.md / .cursorrules,
 *      emitted by `mnemo_guide`.
 *
 * Everything below is used by at least two of them, so the rules cannot drift
 * apart from each other. Tool responses import from here too. */

/** Sent on every session as the server's `instructions`. Kept short on purpose:
 * it is paid for on every connection. */
export const SERVER_INSTRUCTIONS = [
  "mnemo is the user's persistent, per-project memory: plain-text notes in a git store, shared across their machines.",
  "",
  "At the start of work on a known project, call mnemo_load_project so you resume instead of restarting, and print the",
  "card it returns verbatim. Before answering \"what did we decide about X\", search the memory rather than guessing.",
  "Slugs are exact: list projects rather than inventing one.",
  "",
  "This server stores and retrieves; it cannot see this conversation, so deciding what is worth remembering is yours.",
  "Save a fact when it is a decision, a constraint, a gotcha, a known bug or a useful reference — one fact per memory.",
  "Do not save what is already in the code or in git history, or what is ephemeral to this conversation. When in doubt,",
  "save less. Nothing is published to the user's other machines until mnemo_push runs.",
].join("\n");

export const SAVE_CRITERION = [
  "**What to persist.** Decisions made, constraints that must not be broken, gotchas, known bugs, useful references.",
  "Not what is already in the code, in git history, or ephemeral to this conversation. If there is nothing worth",
  "keeping, say so — do not invent a note to have saved something.",
  "",
  "**One fact per file.** If a note mixes two topics, write two. Search first with mnemo_search_memories and update",
  "the existing note instead of adding a near-duplicate.",
  "",
  "**`projects` vs `services`.** `projects` means genuinely different projects that share the same fact — that is the",
  "overlap, and it is rare. Parts of ONE project (repos, modules, areas) go in `services`. Two repos of the same",
  "product are one project with two services, not two projects. Ask the user before tagging a second project.",
].join("\n");

export const PENDING_CRITERION = [
  "**Pending is the project's living state**, what a later session resumes from. `mnemo_write_pending` replaces the",
  "whole file, so load the project first and send the merged result.",
  "",
  "Sections are free-form. `## In progress` and `## Next` feed the card's numbered list; anything else you add",
  "(`## Blocked`, `## Debt`, `## Branches`, `## Deployed`, `## Risks`) renders as its own block. Add only the ones",
  "this project needs; do not copy another project's or force empty ones.",
].join("\n");

export const MACHINE_RULE = [
  "**Machine-bound work.** Memory is shared across the user's machines, but some pending items belong to one only.",
  "Those are stamped `[@<machine>]` and the card marks them ⚠ when they are not from here.",
  "",
  "Do not act on them here: do not look for their repo, do not commit or push them. Say the work belongs to that",
  "machine instead. Before any git or build action on a code repo, check it exists on this machine.",
  "",
  "When saving, stamp `[@<machine>]` ONLY on what is physically tied to one machine: uncommitted changes, a local",
  "unpushed branch, a service running there, a local path. Test: could any machine that has the repo do it? Then it",
  "is portable — do not stamp it. When unsure, do not stamp: a portable item wrongly marked ⚠ is as confusing as a",
  "local one left unmarked.",
].join("\n");

export const CONFLICT_RULE = [
  "**Merging a conflict.** Keep the information from BOTH sides — losing a memory is worse than a redundant note.",
  "For pending.md the right merge is almost always the union of the tasks, minus duplicates, respecting anything",
  "already marked done on either side.",
  "",
  "If the two sides assert contradictory things, STOP and ask the user which one holds. Do not decide that yourself.",
].join("\n");

/** The short form the two-phase tools return in their own responses. The long
 * rule below is built from it, so the guide carries this exact text and the two
 * cannot drift into saying slightly different things. */
export const CONFIRM_NOTE =
  "Show the user exactly what this would change and wait for an explicit yes. Only then call this again with " +
  "`confirm` set to the value above. Never confirm on your own judgement — git is the only undo there is.";

export const CONFIRMATION_RULE = [
  "**Operations that must not act unasked.** `mnemo_push`, `mnemo_rename` and `mnemo_forget` never act on the first",
  "call: they report what would happen and hand back a confirmation value.",
  "",
  CONFIRM_NOTE,
  "",
  "For a secret-scan refusal in particular: the findings name a file and a line with the match redacted. Show them.",
  "A credential published to a hub is not something an acknowledgement can take back.",
].join("\n");

/** The whole criterion, for a rules file. */
export function guideText(): string {
  return [
    "# mnemo — persistent project memory",
    "",
    "The mnemo MCP server holds this user's memory across sessions and machines: plain-text notes in a git store.",
    "The tools store and retrieve; they cannot see the conversation, so the judgement below is yours.",
    "",
    "## Starting work",
    "",
    "Load the project's memory with `mnemo_load_project` before working on something the user has worked on before,",
    "and print the card it returns verbatim, without prose around it. Slugs are exact — call `mnemo_list_projects`",
    "rather than guessing one. Before answering \"what did we decide about X\", search instead of reconstructing.",
    "",
    "## Saving",
    "",
    SAVE_CRITERION,
    "",
    PENDING_CRITERION,
    "",
    "Writes are not committed as they happen. Write what the session produced, then call `mnemo_commit` once, so one",
    "session is one commit. Pushing is separate again: until `mnemo_push` runs, the memory is only on this machine.",
    "",
    "## " + "Machines",
    "",
    MACHINE_RULE,
    "",
    "## Syncing",
    "",
    "Call `mnemo_sync` before writing, so a save does not land on top of a stale version.",
    "",
    CONFLICT_RULE,
    "",
    "## Confirmation",
    "",
    CONFIRMATION_RULE,
    "",
  ].join("\n");
}
