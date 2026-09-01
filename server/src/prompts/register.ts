/** MCP prompts: the criterion, delivered as invocable templates.
 *
 * Claude Code, Cursor and VS Code surface these; Codex does not implement
 * prompts at all, which is why the same rules also go out through the server's
 * `instructions` and through `mnemo_guide`. */

import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { CONFLICT_RULE, MACHINE_RULE, PENDING_CRITERION, SAVE_CRITERION } from "./criterion.js";

const user = (text: string) => ({
  messages: [{ role: "user" as const, content: { type: "text" as const, text } }],
});

export function registerPrompts(server: McpServer): void {
  server.registerPrompt(
    "save_context",
    {
      title: "mnemo: save this session",
      description: "Distil what this session produced into the project's persistent memory, and commit it.",
      argsSchema: { project: z.string().describe("Project slug to save into.") },
    },
    ({ project }) =>
      user(
        [
          `Save what this session produced into the memory of the project \`${project}\`.`,
          "",
          "1. `mnemo_sync` first, so you do not write on top of a stale version.",
          `2. Confirm \`${project}\` exists with \`mnemo_list_projects\`. If it does not, ask the user before creating it.`,
          "3. Review the work and extract only what is worth persisting, per the criterion below. Write each fact with",
          "   `mnemo_write_memory`.",
          "4. Update the project's pending list with `mnemo_write_pending`.",
          "5. `mnemo_commit` once, with a message like `save(<slug>): <short summary>`.",
          "6. Report compactly: what was saved, what changed in pending, the commit. Do not repeat each memory's content",
          "   back at the user — they lived the session.",
          "",
          SAVE_CRITERION,
          "",
          PENDING_CRITERION,
          "",
          MACHINE_RULE,
        ].join("\n"),
      ),
  );

  server.registerPrompt(
    "load_context",
    {
      title: "mnemo: resume a project",
      description: "Load a project's memory — decisions, constraints, gotchas, pending work — and pick up where it was left.",
      argsSchema: { project: z.string().describe("Project slug to load.") },
    },
    ({ project }) =>
      user(
        [
          `Load the memory of \`${project}\` with \`mnemo_load_project\` and get ready to continue that work.`,
          "",
          "Print the card it returns verbatim, as the whole reply, with no prose before or after. You will also receive",
          "the full detail — keep it in context, but unfold it only if the user asks about a decision or a topic.",
          "",
          "If the slug does not exist, list the projects and ask which one they meant. Do not guess, and do not load",
          "\"the last one\".",
          "",
          MACHINE_RULE,
        ].join("\n"),
      ),
  );

  server.registerPrompt(
    "mem",
    {
      title: "mnemo: jot one note",
      description: "Capture a single fact into a project's memory mid-session, without a full save.",
      argsSchema: {
        project: z.string().describe("Project slug, or several comma-separated for a fact that genuinely serves more than one."),
        note: z.string().describe("The fact to remember."),
      },
    },
    ({ project, note }) =>
      user(
        [
          `Save this into the memory of \`${project}\`, as one atomic note:`,
          "",
          note,
          "",
          "Sync first, then `mnemo_write_memory` with a kebab-case id derived from the content, then `mnemo_commit`",
          "with a message like `mem(<slug>): <summary>`. Confirm in one line what was saved and where.",
          "",
          SAVE_CRITERION,
        ].join("\n"),
      ),
  );

  server.registerPrompt(
    "sync_memory",
    {
      title: "mnemo: sync with the hub",
      description: "Pull memory saved on the user's other machines, resolving any conflicts.",
      argsSchema: {},
    },
    () =>
      user(
        [
          "Call `mnemo_sync` to bring in what the user saved from their other machines.",
          "",
          "If it reports conflicts, merge each one and write it back with `mnemo_resolve_conflict`, then finish with",
          "`mnemo_rebase` action `continue`. If the merge is going wrong, `mnemo_rebase` action `abort` puts the store",
          "back exactly as it was — say so rather than leaving it half-merged.",
          "",
          CONFLICT_RULE,
        ].join("\n"),
      ),
  );
}
