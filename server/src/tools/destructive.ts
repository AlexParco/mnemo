/** Rename and forget (P4).
 *
 * Same two-phase shape as `mnemo_push`: called without a confirmation these
 * report what would change and hand back a value; called with it they act. The
 * value is a digest of the recomputed plan, so a store that moved in between
 * stops the operation instead of applying a stale plan.
 *
 * The descriptions carry the part that code cannot check: that the user is shown
 * the plan and says yes before the confirmation is passed back. */

import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { applyForget, applyRename, planForget, planRename } from "../store/destructive.js";
import { storeDir } from "../store/paths.js";
import { guarded, json, ok } from "./result.js";

const CONFIRM_NOTE =
  "Show the user exactly what this would change and wait for an explicit yes. Only then call this again with " +
  "`confirm` set to the value above. Never confirm on your own judgement — git is the only undo there is.";

export function registerDestructiveTools(server: McpServer): void {
  server.registerTool(
    "mnemo_rename",
    {
      title: "mnemo: rename a project's slug",
      description:
        "Change a project's slug — its identity — across the whole store: the directory, INDEX.md, and the `projects` " +
        "field of every memory tagged with it, overlap preserved. Called without `confirm` it only reports what would " +
        "change and returns a confirmation value; nothing is touched until you call it again with that value. " +
        "To change only the readable NAME, this is the wrong tool: that is a one-field edit via mnemo_upsert_project. " +
        "Renaming onto an existing slug is refused — that would be a merge, which this does not do.",
      inputSchema: {
        from: z.string().describe("Current slug."),
        to: z.string().describe("New slug, kebab-case. Must not already exist."),
        confirm: z.string().optional().describe("The confirmation value from the planning call, after the user agreed."),
      },
      annotations: { readOnlyHint: false, destructiveHint: true },
    },
    async ({ from, to, confirm }) =>
      guarded(async () => {
        const store = storeDir();
        if (!confirm) {
          const plan = await planRename(store, from, to);
          return ok(
            `Renaming '${plan.from}' to '${plan.to}' would rewrite the project directory, its INDEX.md, and the ` +
              `\`projects\` field of ${plan.memories.length} memories.`,
            json({ memories: plan.memories, confirm: plan.token }),
            CONFIRM_NOTE,
          );
        }
        const result = await applyRename(store, from, to, confirm);
        return ok(
          `Renamed '${result.from}' to '${result.to}'. ${result.memoriesUpdated.length} memories retagged. ` +
            `Committed ${result.commit.sha}. From now on the project loads as '${result.to}'.`,
          result.commit.hasRemote
            ? "Local only — until this is pushed, the other machines still have the old slug."
            : "This store has no remote.",
        );
      }),
  );

  server.registerTool(
    "mnemo_forget",
    {
      title: "mnemo: delete a project or a memory",
      description:
        "Delete a whole project or a single memory from the store. Called without `confirm` it only reports what " +
        "would go and returns a confirmation value; nothing is deleted until you call it again with that value. " +
        "Deleting a project deletes only the memories tagged with it ALONE — memories it shares with other projects " +
        "are untagged and survive, and the report says which. If the user says 'delete X' without making clear " +
        "whether X is a project or a memory, ask; do not guess what to remove.",
      inputSchema: {
        kind: z.enum(["project", "memory"]),
        target: z.string().describe("Project slug, or memory id."),
        confirm: z.string().optional().describe("The confirmation value from the planning call, after the user agreed."),
      },
      annotations: { readOnlyHint: false, destructiveHint: true },
    },
    async ({ kind, target, confirm }) =>
      guarded(async () => {
        const store = storeDir();
        if (!confirm) {
          const plan = await planForget(store, kind, target);
          if (plan.kind === "forget-memory") {
            return ok(
              `Deleting memory '${plan.id}' — tagged with ${plan.projects.join(", ") || "no project"}.\n"${plan.summary}"`,
              json({ brokenLinks: plan.brokenLinks, confirm: plan.token }),
              plan.brokenLinks.length > 0
                ? `${plan.brokenLinks.length} file(s) link to it with [[${plan.id}]] and would be left dangling; ` +
                  `they are NOT fixed silently. ${CONFIRM_NOTE}`
                : CONFIRM_NOTE,
            );
          }
          return ok(
            `Deleting project '${plan.slug}' (${plan.name}) removes projects/${plan.slug}/ (INDEX.md, pending.md), ` +
              `deletes ${plan.exclusive.length} memories tagged with it alone, and untags ${plan.shared.length} ` +
              `shared with other projects — those survive.`,
            json({ deletes: plan.exclusive, untags: plan.shared, brokenLinks: plan.brokenLinks, confirm: plan.token }),
            CONFIRM_NOTE,
          );
        }
        const result = await applyForget(store, kind, target, confirm);
        const lines = [
          result.kind === "project"
            ? `Deleted project '${result.target}': ${result.deleted.length} memories removed, ${result.untagged.length} untagged and kept.`
            : `Deleted memory '${result.target}'.`,
          `Committed ${result.commit.sha}.`,
        ];
        if (result.untagged.length > 0) {
          lines.push(`Kept: ${result.untagged.map((u) => `${u.id} → ${u.remaining.join(", ")}`).join("; ")}`);
        }
        if (result.brokenLinks.length > 0) {
          lines.push(`Now dangling, left as they are: ${result.brokenLinks.join(", ")}`);
        }
        if (result.commit.hasRemote) lines.push("Local only — the other machines keep what was deleted until this is pushed.");
        return ok(lines.join("\n"));
      }),
  );
}
