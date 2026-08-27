/** Rename and forget: the two operations that can lose memory.
 *
 * Both are two-phase. `plan` reports exactly what would change and returns a
 * token; `apply` recomputes the plan and refuses unless the token still matches.
 * That makes "show the user what will go, then wait for an explicit yes" a
 * property of the API rather than a step in a prompt — and it is client-agnostic,
 * unlike MCP elicitation.
 *
 * The token is a digest of the recomputed plan, so it needs no server state: it
 * survives a restart, cannot be replayed against different content, and is
 * single-use by construction, because applying the plan changes the plan.
 *
 * The rule that must not break: a memory shared with other projects is UNTAGGED,
 * never deleted. Classification reads the parsed `projects` field — a grep for
 * the bare slug matches prose and longer slugs alike. */

import fs from "node:fs";
import { createHash } from "node:crypto";
import { gitStatus } from "../git/read.js";
import { ensureStore } from "./bootstrap.js";
import { parseDocument, renderDocument, setField } from "./frontmatter.js";
import { withLock } from "./lock.js";
import { loadMemories, type Memory } from "./memory.js";
import { indexPath, memoryPath, pendingPath, projectDir } from "./paths.js";
import { projectExists, projectSlugs, readProject } from "./project.js";
import { commitInLock, StoreError, today, type CommitResult } from "./write.js";

const KEBAB = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

export interface RenamePlan {
  kind: "rename";
  from: string;
  to: string;
  /** Memories whose `projects` field would be rewritten. */
  memories: string[];
  token: string;
}

export interface SharedMemory {
  id: string;
  /** The projects it keeps once the slug is removed. */
  remaining: string[];
}

export interface ForgetProjectPlan {
  kind: "forget-project";
  slug: string;
  name: string;
  /** Tagged with this project only: these files are deleted. */
  exclusive: string[];
  /** Tagged with others too: untagged, never deleted. */
  shared: SharedMemory[];
  /** Files that link to a deleted memory with [[id]] and would be left dangling. */
  brokenLinks: string[];
  token: string;
}

export interface ForgetMemoryPlan {
  kind: "forget-memory";
  id: string;
  projects: string[];
  summary: string;
  brokenLinks: string[];
  token: string;
}

export type ForgetPlan = ForgetProjectPlan | ForgetMemoryPlan;
export type Plan = RenamePlan | ForgetPlan;

/** Hash of everything the plan asserts about the store.
 *
 * `apply` rebuilds the plan from the store and hashes it again, so any drift that
 * matters — a new memory tagging the slug, an edited `projects` field, the target
 * gone — changes the token and stops the operation. Git state is deliberately not
 * part of it: a push between plan and apply changes nothing about what would be
 * deleted, and invalidating on it would only be noise. */
function digest(plan: Omit<Plan, "token">): string {
  return createHash("sha256").update(JSON.stringify(plan)).digest("hex").slice(0, 12);
}

/** Files that reference `[[id]]`, anywhere in the store. */
function incomingLinks(store: string, ids: string[], excluding: Set<string>): string[] {
  if (ids.length === 0) return [];
  const needles = ids.map((id) => `[[${id}]]`);
  const hits = new Set<string>();
  for (const memory of loadMemories(store)) {
    if (excluding.has(memory.id)) continue;
    if (needles.some((n) => memory.raw.includes(n))) hits.add(`memories/${memory.id}.md`);
  }
  for (const slug of projectSlugs(store)) {
    for (const [rel, file] of [
      [`projects/${slug}/pending.md`, pendingPath(store, slug)],
      [`projects/${slug}/INDEX.md`, indexPath(store, slug)],
    ] as const) {
      try {
        const raw = fs.readFileSync(file, "utf8");
        if (needles.some((n) => raw.includes(n))) hits.add(rel);
      } catch {
        continue;
      }
    }
  }
  return [...hits].sort();
}

function taggedWith(memories: Memory[], slug: string): Memory[] {
  return memories.filter((m) => m.projects.includes(slug));
}

// --------------------------------------------------------------------- rename

function buildRenamePlan(store: string, from: string, to: string): Omit<RenamePlan, "token"> {
  if (!KEBAB.test(to)) throw new StoreError(`'${to}' is not a valid slug: use lower-case kebab-case.`);
  if (from === to) throw new StoreError("The old and new slugs are the same.");
  if (!projectExists(store, from)) {
    const existing = projectSlugs(store);
    throw new StoreError(
      `No project '${from}' in the store.` + (existing.length > 0 ? ` Existing: ${existing.join(", ")}.` : ""),
    );
  }
  if (fs.existsSync(projectDir(store, to))) {
    throw new StoreError(
      `'${to}' already exists. Renaming onto it would merge two projects, which this operation does not do.`,
    );
  }
  return { kind: "rename", from, to, memories: taggedWith(loadMemories(store), from).map((m) => m.id) };
}

export async function planRename(store: string, from: string, to: string): Promise<RenamePlan> {
  return withLock(store, () => {
    const plan = buildRenamePlan(store, from, to);
    return { ...plan, token: digest(plan) };
  });
}

export interface RenameResult {
  from: string;
  to: string;
  memoriesUpdated: string[];
  commit: CommitResult;
}

export async function applyRename(store: string, from: string, to: string, token: string): Promise<RenameResult> {
  return withLock(store, () => {
    const plan = buildRenamePlan(store, from, to);
    requireFresh(digest(plan), token, "rename");
    requireCleanTree(store);

    const updated = today();
    fs.renameSync(projectDir(store, from), projectDir(store, to));

    const index = parseDocument(fs.readFileSync(indexPath(store, to), "utf8"));
    fs.writeFileSync(indexPath(store, to), renderDocument(setField(setField(index, "slug", to), "updated", updated)));

    for (const id of plan.memories) {
      const file = memoryPath(store, id);
      const doc = parseDocument(fs.readFileSync(file, "utf8"));
      // Rewrite only the projects field, in place: overlap and order survive,
      // and the body's prose is never touched.
      const projects = (Array.isArray(doc.values["projects"]) ? doc.values["projects"] : [doc.values["projects"] as string])
        .map((p) => (p === from ? to : p));
      fs.writeFileSync(file, renderDocument(setField(setField(doc, "projects", projects), "updated", updated)));
    }

    verifyGone(store, from);
    if (!projectExists(store, to)) throw new StoreError(`Rename left no project at '${to}'.`);

    return {
      from, to, memoriesUpdated: plan.memories,
      commit: commitInLock(store, `rename(${from} → ${to}): ${plan.memories.length} memories retagged`),
    };
  });
}

// --------------------------------------------------------------------- forget

function buildForgetProjectPlan(store: string, slug: string): Omit<ForgetProjectPlan, "token"> {
  const project = readProject(store, slug);
  if (!project) {
    const existing = projectSlugs(store);
    throw new StoreError(
      `No project '${slug}' in the store.` + (existing.length > 0 ? ` Existing: ${existing.join(", ")}.` : ""),
    );
  }
  const tagged = taggedWith(loadMemories(store), slug);
  const exclusive = tagged.filter((m) => m.projects.length === 1).map((m) => m.id);
  const shared: SharedMemory[] = tagged
    .filter((m) => m.projects.length > 1)
    .map((m) => ({ id: m.id, remaining: m.projects.filter((p) => p !== slug) }));
  return {
    kind: "forget-project",
    slug,
    name: project.name,
    exclusive,
    shared,
    brokenLinks: incomingLinks(store, exclusive, new Set(exclusive)),
  };
}

function buildForgetMemoryPlan(store: string, id: string): Omit<ForgetMemoryPlan, "token"> {
  const memory = loadMemories(store).find((m) => m.id === id);
  if (!memory) throw new StoreError(`No memory '${id}'. Search for it before assuming it is gone.`);
  return {
    kind: "forget-memory",
    id,
    projects: memory.projects,
    summary: memory.summary,
    brokenLinks: incomingLinks(store, [id], new Set([id])),
  };
}

export async function planForget(store: string, kind: "project" | "memory", id: string): Promise<ForgetPlan> {
  return withLock(store, () => {
    const plan = kind === "project" ? buildForgetProjectPlan(store, id) : buildForgetMemoryPlan(store, id);
    return { ...plan, token: digest(plan) } as ForgetPlan;
  });
}

export interface ForgetResult {
  kind: "project" | "memory";
  target: string;
  deleted: string[];
  untagged: SharedMemory[];
  brokenLinks: string[];
  commit: CommitResult;
}

export async function applyForget(store: string, kind: "project" | "memory", id: string, token: string): Promise<ForgetResult> {
  return withLock(store, () => {
    if (kind === "memory") {
      const plan = buildForgetMemoryPlan(store, id);
      requireFresh(digest(plan), token, "forget");
      requireCleanTree(store);
      fs.rmSync(memoryPath(store, id));
      return {
        kind, target: id, deleted: [id], untagged: [], brokenLinks: plan.brokenLinks,
        commit: commitInLock(store, `forget(memory ${id})`),
      };
    }

    const plan = buildForgetProjectPlan(store, id);
    requireFresh(digest(plan), token, "forget");
    requireCleanTree(store);
    const updated = today();

    // Untag BEFORE deleting: if anything throws, the shared memories are the
    // side that must survive, and they are already safe.
    for (const { id: memoryId, remaining } of plan.shared) {
      const file = memoryPath(store, memoryId);
      const doc = parseDocument(fs.readFileSync(file, "utf8"));
      fs.writeFileSync(file, renderDocument(setField(setField(doc, "projects", remaining), "updated", updated)));
    }
    for (const memoryId of plan.exclusive) fs.rmSync(memoryPath(store, memoryId));
    fs.rmSync(projectDir(store, id), { recursive: true });

    verifyGone(store, id);
    for (const { id: memoryId, remaining } of plan.shared) {
      const survivor = loadMemories(store).find((m) => m.id === memoryId);
      if (!survivor) throw new StoreError(`A shared memory was deleted: '${memoryId}'. This is a bug.`);
      if (survivor.projects.join(",") !== remaining.join(",")) {
        throw new StoreError(`Untagging '${memoryId}' left ${JSON.stringify(survivor.projects)}, expected ${JSON.stringify(remaining)}.`);
      }
    }

    return {
      kind, target: id, deleted: plan.exclusive, untagged: plan.shared, brokenLinks: plan.brokenLinks,
      commit: commitInLock(store, `forget(project ${id}): ${plan.exclusive.length} deleted, ${plan.shared.length} untagged`),
    };
  });
}

// ---------------------------------------------------------------------- guards

function requireFresh(expected: string, given: string, what: string): void {
  if (expected !== given) {
    throw new StoreError(
      `That confirmation does not match the current state of the store — something changed since the plan was made, ` +
        `or it was not the value issued. Nothing was ${what === "rename" ? "renamed" : "deleted"}. ` +
        `Run the plan again, show the user what it now says, and confirm from there.`,
    );
  }
}

/** Structural changes get their own commit, so the store's history stays
 * readable — git is the only undo there is. A dirty worktree would be swept into
 * it, so it is refused instead. */
function requireCleanTree(store: string): void {
  ensureStore(store);
  if (gitStatus(store).dirty) {
    throw new StoreError(
      "The store has uncommitted changes. Commit them first with mnemo_commit: this operation makes its own commit, " +
        "and would otherwise fold unrelated work into it.",
    );
  }
}

/** Post-condition: no memory still lists the slug as a project. Read from the
 * parsed field, never from a grep of the bare slug. */
function verifyGone(store: string, slug: string): void {
  const left = loadMemories(store).filter((m) => m.projects.includes(slug));
  if (left.length > 0) {
    throw new StoreError(`'${slug}' is still tagged on: ${left.map((m) => m.id).join(", ")}. This is a bug.`);
  }
  if (fs.existsSync(projectDir(store, slug))) throw new StoreError(`projects/${slug}/ still exists. This is a bug.`);
}
