/** The sync surface (P3): pull, conflict resolution, push.
 *
 * Two rules cannot be enforced in code and so are stated in the descriptions,
 * where every host can read them: a conflict is merged by keeping BOTH sides,
 * and a semantic clash — two sides asserting contradictory things — stops and
 * asks the user rather than picking a winner. Losing a memory is worse than
 * keeping a redundant one. */

import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { pushStore, rebaseAction, resolveConflict, syncStore } from "../git/sync.js";
import { storeDir } from "../store/paths.js";
import { guarded, json, ok } from "./result.js";

export function registerSyncTools(server: McpServer): void {
  server.registerTool(
    "mnemo_sync",
    {
      title: "mnemo: pull from the hub",
      description:
        "Bring in memory saved from the user's other machines (git pull --rebase --autostash). Run it BEFORE writing " +
        "anything, so a save does not land on top of a stale version, and whenever the user asks what is new. If it " +
        "comes back with conflicts, resolve each one with mnemo_resolve_conflict and then call mnemo_rebase — the " +
        "store is mid-rebase until you do, and nothing else will work.",
      inputSchema: {},
      annotations: { readOnlyHint: false, idempotentHint: true },
    },
    async () =>
      guarded(async () => {
        const result = await syncStore(storeDir());
        if (result.conflicts.length === 0) return ok(result.detail);
        const rules =
          "Merge each file keeping the information from BOTH sides — losing a memory is worse than a redundant note. " +
          "For pending.md the right merge is almost always the union of the tasks, minus duplicates, respecting " +
          "anything already marked done on either side. If the two sides assert contradictory things, STOP and ask " +
          "the user which one holds; do not decide that yourself.";
        return ok(
          `${result.detail}\n\n${result.conflicts.length} conflicted file(s): ${result.conflicts.map((c) => c.file).join(", ")}`,
          json(result.conflicts),
          rules,
        );
      }),
  );

  server.registerTool(
    "mnemo_resolve_conflict",
    {
      title: "mnemo: resolve one conflicted file",
      description:
        "Write the merged version of a file left conflicted by mnemo_sync, and stage it. Send the complete file with " +
        "the conflict markers gone and both sides' content preserved; content that still carries markers is refused.",
      inputSchema: {
        file: z.string().describe("Store-relative path, as reported by mnemo_sync."),
        content: z.string().describe("The complete merged file."),
      },
      annotations: { readOnlyHint: false, destructiveHint: true },
    },
    async ({ file, content }) =>
      guarded(async () => {
        const { remaining } = await resolveConflict(storeDir(), file, content);
        return ok(
          remaining.length === 0
            ? `Resolved '${file}'. Nothing left conflicted — call mnemo_rebase with action "continue".`
            : `Resolved '${file}'. Still conflicted: ${remaining.join(", ")}.`,
        );
      }),
  );

  server.registerTool(
    "mnemo_rebase",
    {
      title: "mnemo: continue or abort the rebase",
      description:
        "Finish the rebase a sync started. Use \"continue\" once every conflicted file is resolved. Use \"abort\" to " +
        "put the store back exactly as it was before the sync and tell the user — the safe way out when a merge is " +
        "going wrong.",
      inputSchema: { action: z.enum(["continue", "abort"]) },
      annotations: { readOnlyHint: false, destructiveHint: true },
    },
    async ({ action }) =>
      guarded(async () => {
        const result = await rebaseAction(storeDir(), action);
        return ok(result.detail);
      }),
  );

  server.registerTool(
    "mnemo_push",
    {
      title: "mnemo: publish the store to the hub",
      description:
        "Publish local commits so the user's other machines can see them. Until this runs, saved memory exists only " +
        "on this machine. Every push is scanned for secrets first and there is no way around that scan. If it finds " +
        "something, the push is refused and you get the findings plus an acknowledgement value: SHOW THE FINDINGS TO " +
        "THE USER and only pass the acknowledgement back if they confirm it is a false positive. Never acknowledge on " +
        "your own judgement. Unless MNEMO_AUTOPUSH is set, confirm with the user before pushing at all.",
      inputSchema: {
        acknowledge: z
          .string()
          .optional()
          .describe("The value returned with a secret-scan refusal, passed back after the user confirmed a false positive."),
      },
      annotations: { readOnlyHint: false, destructiveHint: false },
    },
    async ({ acknowledge }) =>
      guarded(async () => {
        const result = await pushStore(storeDir(), acknowledge);
        if (result.pushed) return ok(result.detail);
        if (result.findings.length > 0 && (result.refused === "secrets" || result.refused === "bad-acknowledgement")) {
          return ok(
            result.detail,
            json(result.findings),
            `The matched text is redacted above; open the file to check it. If the user confirms these are false ` +
              `positives, call mnemo_push again with acknowledge: "${result.acknowledgeToken}". That value stops ` +
              `working as soon as the commits or the findings change.`,
          );
        }
        return ok(result.detail);
      }),
  );
}
