/** The read-only tool surface (P1).
 *
 * Tool descriptions carry more weight here than they did in the plugin: outside
 * Claude Code there is no SKILL.md to set the rules, so whatever an agent needs
 * to know has to travel in the description or in the response itself. That is
 * why `mnemo_load_project` returns the machine rule alongside the card — an
 * agent acting on another machine's pending work is the failure that actually
 * costs the user something. */

import fs from "node:fs";
import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { gitStatus } from "../git/read.js";
import { machineLabel, memoryPath, storeDir } from "../store/paths.js";
import { loadProjectContext, overview, searchMemories } from "../store/store.js";
import { projectSlugs } from "../store/project.js";
import { parseMemory } from "../store/memory.js";
import { machineFlag } from "../store/pending.js";
import { resolveLang } from "../card/render.js";
import { MACHINE_RULE } from "../prompts/criterion.js";
import { fail, isToolResult, json, ok, type ToolResult } from "./result.js";

const NO_STORE =
  "There is no mnemo store yet. It is created by the first save (see mnemo's save-context flow). " +
  "To adopt an existing store from another machine, set MNEMO_REMOTE and clone it.";

function requireStore(): { store: string } | ToolResult {
  const store = storeDir();
  if (!fs.existsSync(store)) return fail(`${NO_STORE}\nExpected location: ${store}`);
  return { store };
}

function unknownProject(store: string, slug: string): ToolResult {
  const slugs = projectSlugs(store);
  return fail(
    `No project '${slug}' in the store.\n` +
      (slugs.length > 0
        ? `Existing slugs: ${slugs.join(", ")}\nPick one; do not guess or invent a project.`
        : "The store has no projects yet."),
  );
}

export function registerReadTools(server: McpServer): void {
  server.registerTool(
    "mnemo_status",
    {
      title: "mnemo: store status",
      description:
        "Where the mnemo memory store stands: its path, whether it is wired to a hub (git remote), how many commits " +
        "are waiting to be pushed, and this machine's label. Use it when the user asks about their memory setup, or " +
        "before telling them whether something is synced to their other machines.",
      inputSchema: {},
      annotations: { readOnlyHint: true },
    },
    async () => {
      const store = storeDir();
      const exists = fs.existsSync(store);
      const git = exists ? gitStatus(store) : null;
      const o = exists ? overview(store) : null;
      const lines = [
        `store: ${store}${exists ? "" : "  (does not exist yet)"}`,
        `machine: ${machineLabel()}`,
      ];
      if (git) {
        lines.push(
          `git repo: ${git.isRepo ? "yes" : "no"}`,
          `remote: ${git.remoteUrl ?? "none — this store is local only"}`,
          `branch: ${git.branch ?? "?"}`,
          `uncommitted changes: ${git.dirty ? "yes" : "no"}`,
          `unpushed commits: ${git.unpushed === null ? "unknown (no upstream)" : git.unpushed}`,
        );
        if (git.rebaseInProgress) lines.push("⚠ a rebase is in progress — the store is mid-merge and needs resolving");
      }
      if (o) lines.push(`projects: ${o.totals.projects} · memories: ${o.totals.memories}`);
      lines.push(
        process.env.MNEMO_AUTOPUSH
          ? "autopush: on — the user has opted into pushing without being asked each time"
          : "autopush: off — confirm with the user before calling mnemo_push",
      );
      if (!exists) lines.push("", NO_STORE);
      return ok(lines.join("\n"));
    },
  );

  server.registerTool(
    "mnemo_list_projects",
    {
      title: "mnemo: list projects",
      description:
        "Overview of every project in the mnemo memory store: slug, status, how many memories each has, and which " +
        "services it touches. Read-only. Use it when the user asks what projects they have, or when they name a " +
        "project loosely and you need the exact slug before loading it.",
      inputSchema: {},
      annotations: { readOnlyHint: true },
    },
    async () => {
      const guard = requireStore();
      if (isToolResult(guard)) return guard;
      const o = overview(guard.store);
      if (o.projects.length === 0) {
        return ok("The store has no projects yet. The first one is created by a save.");
      }
      const rows = o.projects.map((p) => {
        const memories = p.shared > 0 ? `${p.memories} (${p.shared} shared)` : String(p.memories);
        return `| ${p.slug} | ${p.status} | ${memories} | ${p.services.join(", ") || "—"} | ${p.updated || "—"} |`;
      });
      const table = [
        "| project (slug) | status | memories | services | updated |",
        "|---|---|---|---|---|",
        ...rows,
      ].join("\n");
      const orphans = o.totals.orphanMemories > 0 ? ` · ${o.totals.orphanMemories} orphan (tagged to no existing project)` : "";
      const summary = `${o.totals.projects} projects · ${o.totals.memories} memories · ${o.totals.sharedFiles} files in shared/${orphans}`;
      return ok(`${table}\n\n${summary}`);
    },
  );

  server.registerTool(
    "mnemo_load_project",
    {
      title: "mnemo: load project memory",
      description:
        "Load a project's persistent memory so work can resume where it was left off: decisions, constraints, " +
        "gotchas, bugs and pending tasks saved in earlier sessions, possibly on another machine. Requires an exact " +
        "slug — call mnemo_list_projects first if you do not have one, and never guess. The FIRST content block is a " +
        "rendered resume card: show it to the user verbatim, and nothing else, unless they ask for the detail. The " +
        "remaining blocks are for you, not for printing.",
      inputSchema: {
        slug: z.string().describe("Exact project slug, as listed by mnemo_list_projects."),
        lang: z.enum(["en", "es"]).optional().describe("Language for the card. Defaults to $MNEMO_LANG, else English."),
      },
      annotations: { readOnlyHint: true },
    },
    async ({ slug, lang }) => {
      const guard = requireStore();
      if (isToolResult(guard)) return guard;
      const ctx = loadProjectContext(guard.store, slug, resolveLang(lang));
      if (!ctx) return unknownProject(guard.store, slug);

      const detail = json({
        project: {
          slug: ctx.project.slug,
          name: ctx.project.name,
          status: ctx.project.status,
          services: ctx.project.services,
          updated: ctx.project.updated,
          description: ctx.project.description,
        },
        pending: ctx.project.pending,
        memories: ctx.memories.map((m) => ({
          id: m.id,
          type: m.type,
          projects: m.projects,
          services: m.services,
          tags: m.tags,
          updated: m.updated,
          body: m.body.trim(),
        })),
        shared: ctx.shared,
      });

      const stamped = ctx.project.pending.flatMap((s) => s.items).filter((i) => /\[@([^\]]+)\]/.test(i.text));
      const foreign = stamped.filter((i) => machineFlag(i.text, ctx.machine) !== "");
      const rule =
        `This machine is '${ctx.machine}'. ${foreign.length} pending item(s) belong to another machine and are ` +
        `marked ⚠ in the card.\n\n${MACHINE_RULE}`;

      return ok(ctx.card, `Detail (do not print unless asked):\n${detail}`, rule);
    },
  );

  server.registerTool(
    "mnemo_search_memories",
    {
      title: "mnemo: search memories",
      description:
        "Search saved facts across the mnemo memory store — decisions, constraints, gotchas, bugs, references, todos. " +
        "Use it when the user asks what was decided about something, and always before writing a new memory, so an " +
        "existing note gets updated instead of duplicated.",
      inputSchema: {
        query: z.string().optional().describe("Case-insensitive text match over id, body, tags, services and projects."),
        project: z.string().optional().describe("Restrict to memories tagged with this project slug."),
        type: z.enum(["decision", "constraint", "gotcha", "bug", "reference", "todo"]).optional(),
        limit: z.number().int().min(1).max(200).optional().describe("Maximum results (default 25)."),
      },
      annotations: { readOnlyHint: true },
    },
    async ({ query, project, type, limit }) => {
      const guard = requireStore();
      if (isToolResult(guard)) return guard;
      const found = searchMemories(guard.store, { query, project, type, limit });
      if (found.length === 0) return ok("No memories matched.");
      return ok(
        json(
          found.map((m) => ({
            id: m.id,
            type: m.type,
            projects: m.projects,
            services: m.services,
            updated: m.updated,
            summary: m.summary,
          })),
        ),
        "Use mnemo_read_memory for the full body of any of these.",
      );
    },
  );

  server.registerTool(
    "mnemo_read_memory",
    {
      title: "mnemo: read one memory",
      description: "Read a single memory in full, by id (the file name without .md), including its frontmatter tags.",
      inputSchema: { id: z.string().describe("Memory id, e.g. 'checkout-customer-immutable'.") },
      annotations: { readOnlyHint: true },
    },
    async ({ id }) => {
      const guard = requireStore();
      if (isToolResult(guard)) return guard;
      const file = memoryPath(guard.store, id);
      let raw: string;
      try {
        raw = fs.readFileSync(file, "utf8");
      } catch {
        return fail(`No memory '${id}'. Search for it with mnemo_search_memories before assuming it does not exist.`);
      }
      const m = parseMemory(raw, file);
      return ok(raw, json({ id: m.id, type: m.type, projects: m.projects, services: m.services, tags: m.tags, updated: m.updated, author: m.author }));
    },
  );
}
