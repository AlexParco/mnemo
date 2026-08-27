/** The write surface (P2).
 *
 * These tools hold the mechanical half of the SCHEMA contract. The other half —
 * what is worth persisting at all — cannot be enforced from here, so it lives in
 * the descriptions: they are the only copy of the rules that reaches a host with
 * no SKILL.md. Distilling a session is still the agent's job; this server only
 * refuses to store something malformed. */

import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { ensureStore } from "../store/bootstrap.js";
import { storeDir } from "../store/paths.js";
import { commitStore, upsertProject, writeMemory, writePending } from "../store/write.js";
import { withLock } from "../store/lock.js";
import { guarded, json, ok } from "./result.js";

const MEMORY_TYPES = ["decision", "constraint", "gotcha", "bug", "reference", "todo"] as const;

function bootstrapLines(store: string): string[] {
  const report = ensureStore(store);
  const lines: string[] = [];
  if (report.createdStore) lines.push(`Created the store at ${store}. Set MNEMO_REMOTE to sync it across machines.`);
  if (report.adoptedHistory) lines.push("Adopted the existing memory from the hub — this machine now shares it.");
  else if (report.wiredRemote) lines.push(`Wired remote: ${report.wiredRemote}`);
  if (report.adoptionBlocked) lines.push(`⚠ ${report.adoptionBlocked}`);
  if (report.wroteTemplates.length > 0) lines.push(`Wrote: ${report.wroteTemplates.join(", ")}`);
  if (!report.identity) {
    lines.push("⚠ git has no committer identity here; set user.name and user.email or commits will fail.");
  }
  return lines;
}

export function registerWriteTools(server: McpServer): void {
  server.registerTool(
    "mnemo_bootstrap",
    {
      title: "mnemo: create or adopt the store",
      description:
        "Create the mnemo memory store, or adopt one that already exists on the hub. Safe to call repeatedly: it only " +
        "adds what is missing. The write tools call it themselves, so use this only when the user explicitly wants to " +
        "set up or inspect the provisioning — for instance on a new machine, after exporting MNEMO_REMOTE.",
      inputSchema: {},
      annotations: { readOnlyHint: false, idempotentHint: true },
    },
    async () =>
      guarded(async () => {
        const store = storeDir();
        const lines = await withLock(store, () => bootstrapLines(store));
        return ok(lines.length > 0 ? lines.join("\n") : `The store at ${store} was already set up; nothing to do.`);
      }),
  );

  server.registerTool(
    "mnemo_upsert_project",
    {
      title: "mnemo: create or update a project",
      description:
        "Create a project in the memory store, or update its name, status, services or description. Creating one is " +
        "deliberate: never invent a project to make a write succeed — if a slug does not exist, ask the user whether " +
        "to create it or fix the slug. Use `services` for the parts of ONE project (repos, modules, areas); two repos " +
        "of the same product are one project with two services, not two projects.",
      inputSchema: {
        slug: z.string().describe("Kebab-case identity of the project. Stable: renaming it later is a separate operation."),
        name: z.string().optional().describe("Readable name. Required when creating."),
        status: z.enum(["active", "paused", "done"]).optional(),
        services: z.array(z.string()).optional().describe("Areas, repos or modules this project touches."),
        description: z.string().optional().describe("What the project is, its goal and scope — context not derivable from the code."),
      },
      annotations: { readOnlyHint: false, idempotentHint: true },
    },
    async (input) =>
      guarded(async () => {
        const store = storeDir();
        const result = await upsertProject(store, input);
        return ok(
          `${result.created ? "Created" : "Updated"} project '${result.slug}' (${result.path}).` +
            (result.created ? " Its pending.md is empty; sections are free-form, add only the ones that apply." : ""),
          "Not committed yet — call mnemo_commit when the batch of writes is done.",
        );
      }),
  );

  server.registerTool(
    "mnemo_write_memory",
    {
      title: "mnemo: save one atomic memory",
      description:
        "Persist ONE fact to the memory store: a decision made, a constraint that must not be broken, a gotcha, a known " +
        "bug, a useful reference. One fact per file — if a note mixes two topics, write two memories. Do NOT save what " +
        "is already in the code, in git history, or is ephemeral to this conversation; when in doubt, save less. " +
        "Search with mnemo_search_memories first and update the existing note instead of adding a near-duplicate. " +
        "`projects` is for genuinely different projects that share the fact (overlap); parts of a single project go in " +
        "`services`. Writes are not committed — call mnemo_commit afterwards.",
      inputSchema: {
        id: z.string().describe("Kebab-case id derived from the content, and the file name. Choose it yourself; it must be unique and self-explanatory."),
        projects: z.array(z.string()).min(1).describe("Project slugs this fact belongs to. Usually one. Every slug must already exist."),
        type: z.enum(MEMORY_TYPES),
        body: z.string().describe("The fact, in markdown. Self-explanatory and concise. Link related memories with [[other-id]]."),
        services: z.array(z.string()).optional().describe("Which repo/module/area within the project this touches."),
        tags: z.array(z.string()).optional(),
        author: z.string().optional().describe("Defaults to the store's git user.name."),
        overwrite: z.boolean().optional().describe("Required to replace an existing memory. Read it first and merge; a fresh body replaces everything."),
      },
      annotations: { readOnlyHint: false, destructiveHint: false },
    },
    async (input) =>
      guarded(async () => {
        const store = storeDir();
        const result = await writeMemory(store, input);
        const blocks = [
          `${result.created ? "Wrote" : "Updated"} memory '${result.id}' (${result.path}). Not committed yet.`,
        ];
        if (result.related.length > 0) {
          blocks.push(
            `These existing memories share vocabulary with it — check you are not splitting one fact in two: ${result.related.join(", ")}`,
          );
        }
        return ok(...blocks);
      }),
  );

  server.registerTool(
    "mnemo_write_pending",
    {
      title: "mnemo: replace a project's pending list",
      description:
        "Replace `pending.md` for a project — the living state that a later session resumes from. This REPLACES the " +
        "whole file, so load the project first and send the merged result. Sections are free-form: `## In progress` and " +
        "`## Next` feed the resume card's numbered list, and anything else you add (`## Blocked`, `## Debt`, " +
        "`## Branches`, `## Deployed`, `## Risks`) renders as its own block. Add only the sections this project needs. " +
        "Stamp an item `[@<machine>]` ONLY when it is physically bound to one machine (uncommitted changes, a local " +
        "branch, a service running there). Test: could any machine with the repo do it? Then it is portable — do not " +
        "stamp it. When unsure, do not stamp.",
      inputSchema: {
        slug: z.string(),
        content: z.string().describe("The complete new pending.md, in markdown."),
      },
      annotations: { readOnlyHint: false, destructiveHint: true },
    },
    async ({ slug, content }) =>
      guarded(async () => {
        const store = storeDir();
        const result = await writePending(store, slug, content);
        return ok(
          `Rewrote pending.md for '${slug}'. Not committed yet.`,
          json(result.sections),
        );
      }),
  );

  server.registerTool(
    "mnemo_commit",
    {
      title: "mnemo: commit the store",
      description:
        "Commit everything written to the memory store since the last commit — normally once, after a batch of memory " +
        "and pending writes, so one session becomes one commit. The commit stays local: pushing it to the hub is a " +
        "separate step, and until then the user's other machines do not see it.",
      inputSchema: {
        message: z.string().describe("Commit message, e.g. \"save(orion-api): token rotation decisions\"."),
      },
      annotations: { readOnlyHint: false, destructiveHint: false },
    },
    async ({ message }) =>
      guarded(async () => {
        const store = storeDir();
        const result = await commitStore(store, message);
        if (!result.committed) return ok("Nothing to commit: the store has no pending changes.");
        const lines = [`Committed ${result.sha} · ${result.files.length} file(s):`, ...result.files.map((f) => `  ${f}`)];
        if (result.strippedTrailers > 0) {
          lines.push(`Removed ${result.strippedTrailers} Co-Authored-By trailer(s): the store does not carry them.`);
        }
        lines.push(
          result.hasRemote
            ? `Local only. ${result.unpushed ?? "?"} commit(s) not on the hub yet — the other machines will not see this until it is pushed.`
            : "This store has no remote, so it lives only on this machine.",
        );
        return ok(lines.join("\n"));
      }),
  );
}
