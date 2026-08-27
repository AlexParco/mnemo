/** Write operations. Every one of them takes the store lock.
 *
 * What is enforced here — rather than asked of the model — is the part of the
 * SCHEMA contract that is mechanical: id shape, `id` == file name, a project has
 * to exist before you can tag a memory with it, and unknown frontmatter survives
 * an update untouched. What is NOT enforced is what deserves to be written at
 * all; that judgement needs the conversation, which this process cannot see. */

import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { gitIdentity, gitTry, gitRun } from "../git/exec.js";
import { gitStatus } from "../git/read.js";
import { ensureStore, type BootstrapReport } from "./bootstrap.js";
import { formatFmValue, parseDocument, renderDocument, setField } from "./frontmatter.js";
import { withLock } from "./lock.js";
import { loadMemories, TYPE_ORDER } from "./memory.js";
import { indexPath, memoryPath, pendingPath, projectDir } from "./paths.js";
import { parsePending, type PendingSection } from "./pending.js";
import { projectExists, projectSlugs, readProject } from "./project.js";

export type MemoryType = (typeof TYPE_ORDER)[number];

/** A refusal the caller can act on: the message says what to do instead. */
export class StoreError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "StoreError";
  }
}

const KEBAB = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

/** Local calendar date, the granularity SCHEMA.md's `updated` uses. */
export function today(now: Date = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

function requireKebab(value: string, what: string): void {
  if (!KEBAB.test(value)) {
    throw new StoreError(`'${value}' is not a valid ${what}: use lower-case kebab-case, e.g. 'checkout-customer-immutable'.`);
  }
}

function requireProjects(store: string, slugs: string[]): void {
  if (slugs.length === 0) throw new StoreError("A memory needs at least one project in `projects`.");
  const missing = slugs.filter((s) => !projectExists(store, s));
  if (missing.length > 0) {
    const existing = projectSlugs(store);
    throw new StoreError(
      `No project(s) ${missing.map((m) => `'${m}'`).join(", ")} in the store. ` +
        (existing.length > 0 ? `Existing: ${existing.join(", ")}. ` : "") +
        "Fix the slug, or create the project with mnemo_upsert_project first — never silently.",
    );
  }
}

export function defaultAuthor(store: string): string {
  return gitIdentity(store)?.name ?? os.userInfo().username;
}

export interface WriteMemoryInput {
  id: string;
  projects: string[];
  type: MemoryType;
  body: string;
  services?: string[];
  tags?: string[];
  author?: string;
  /** Required to replace an existing memory; without it an existing id is refused. */
  overwrite?: boolean;
}

export interface WriteMemoryResult {
  id: string;
  path: string;
  created: boolean;
  /** Existing memories that share vocabulary with this id — possible duplicates. */
  related: string[];
}

/** Memories whose id shares words with `id`, most overlap first. A cheap nudge
 * toward updating an existing note instead of adding a near-duplicate. */
function relatedIds(store: string, id: string, limit = 5): string[] {
  const terms = id.split("-").filter((t) => t.length >= 4);
  if (terms.length === 0) return [];
  return loadMemories(store)
    .filter((m) => m.id !== id)
    .map((m) => {
      const haystack = `${m.id}\n${m.body}`.toLowerCase();
      return { id: m.id, score: terms.filter((t) => haystack.includes(t)).length };
    })
    .filter((m) => m.score > 0)
    .sort((a, b) => b.score - a.score)
    .slice(0, limit)
    .map((m) => m.id);
}

function memoryText(input: Required<Pick<WriteMemoryInput, "id" | "projects" | "type" | "body">> & {
  services: string[];
  tags: string[];
  author: string;
  updated: string;
}): string {
  const lines = [`id: ${input.id}`, `projects: ${formatFmValue(input.projects)}`];
  if (input.services.length > 0) lines.push(`services: ${formatFmValue(input.services)}`);
  if (input.tags.length > 0) lines.push(`tags: ${formatFmValue(input.tags)}`);
  lines.push(`type: ${input.type}`, `author: ${input.author}`, `updated: ${input.updated}`);
  return `---\n${lines.join("\n")}\n---\n\n${input.body.trim()}\n`;
}

export async function writeMemory(store: string, input: WriteMemoryInput): Promise<WriteMemoryResult> {
  requireKebab(input.id, "memory id");
  if (!(TYPE_ORDER as readonly string[]).includes(input.type)) {
    throw new StoreError(`'${input.type}' is not a memory type. Use one of: ${TYPE_ORDER.join(", ")}.`);
  }
  if (input.body.trim() === "") throw new StoreError("A memory needs a body: one fact, self-explanatory and concise.");

  return withLock(store, () => {
    ensureStore(store);
    requireProjects(store, input.projects);

    const file = memoryPath(store, input.id);
    const exists = fs.existsSync(file);
    if (exists && !input.overwrite) {
      throw new StoreError(
        `A memory '${input.id}' already exists. Read it with mnemo_read_memory, merge what you want to keep, ` +
          "and write it again with overwrite: true. Writing a fresh body would drop whatever it already holds.",
      );
    }

    const updated = today();
    if (exists) {
      // Surgical update: unknown frontmatter keys and the original author survive.
      const raw = fs.readFileSync(file, "utf8");
      let doc = parseDocument(raw);
      doc = setField(doc, "id", input.id);
      doc = setField(doc, "projects", input.projects);
      if (input.services) doc = setField(doc, "services", input.services);
      if (input.tags) doc = setField(doc, "tags", input.tags);
      doc = setField(doc, "type", input.type);
      if (input.author) doc = setField(doc, "author", input.author);
      doc = setField(doc, "updated", updated);
      fs.writeFileSync(file, renderDocument({ ...doc, body: `\n\n${input.body.trim()}\n` }));
    } else {
      fs.writeFileSync(
        file,
        memoryText({
          id: input.id,
          projects: input.projects,
          type: input.type,
          body: input.body,
          services: input.services ?? [],
          tags: input.tags ?? [],
          author: input.author ?? defaultAuthor(store),
          updated,
        }),
      );
    }

    return { id: input.id, path: file, created: !exists, related: exists ? [] : relatedIds(store, input.id) };
  });
}

export interface WritePendingResult {
  slug: string;
  path: string;
  sections: Array<{ label: string; open: number; done: number }>;
}

/** Replace a project's `pending.md` wholesale. Sections stay free-form: this
 * writes what it is given and only reports back what it parsed. */
export async function writePending(store: string, slug: string, content: string): Promise<WritePendingResult> {
  return withLock(store, () => {
    ensureStore(store);
    if (!projectExists(store, slug)) requireProjects(store, [slug]);
    const file = pendingPath(store, slug);
    fs.writeFileSync(file, content.endsWith("\n") ? content : `${content}\n`);
    const summarise = (s: PendingSection) => ({
      label: s.label,
      open: s.items.filter((i) => !i.done).length,
      done: s.items.filter((i) => i.done).length,
    });
    return { slug, path: file, sections: parsePending(content).map(summarise) };
  });
}

export interface UpsertProjectInput {
  slug: string;
  name?: string;
  status?: "active" | "paused" | "done";
  services?: string[];
  description?: string;
}

export interface UpsertProjectResult {
  slug: string;
  created: boolean;
  path: string;
  bootstrap: BootstrapReport;
}

export async function upsertProject(store: string, input: UpsertProjectInput): Promise<UpsertProjectResult> {
  requireKebab(input.slug, "project slug");
  return withLock(store, () => {
    const bootstrap = ensureStore(store);
    const existing = readProject(store, input.slug);
    const file = indexPath(store, input.slug);
    const updated = today();

    if (!existing) {
      if (!input.name) throw new StoreError(`Creating project '${input.slug}' needs a readable name.`);
      fs.mkdirSync(projectDir(store, input.slug), { recursive: true });
      const fm = [
        `slug: ${input.slug}`,
        `name: ${input.name}`,
        `status: ${input.status ?? "active"}`,
        `services: ${formatFmValue(input.services ?? [])}`,
        `updated: ${updated}`,
      ];
      const body = input.description?.trim() ?? "";
      fs.writeFileSync(file, `---\n${fm.join("\n")}\n---\n\n# ${input.name}\n${body ? `\n${body}\n` : ""}`);
      fs.writeFileSync(pendingPath(store, input.slug), `# Pending — ${input.name}\n`);
      return { slug: input.slug, created: true, path: file, bootstrap };
    }

    let doc = parseDocument(fs.readFileSync(file, "utf8"));
    if (input.name) doc = setField(doc, "name", input.name);
    if (input.status) doc = setField(doc, "status", input.status);
    if (input.services) doc = setField(doc, "services", input.services);
    doc = setField(doc, "updated", updated);
    if (input.description !== undefined) doc = { ...doc, body: `\n\n# ${input.name ?? existing.name}\n\n${input.description.trim()}\n` };
    fs.writeFileSync(file, renderDocument(doc));
    return { slug: input.slug, created: false, path: file, bootstrap };
  });
}

export interface CommitResult {
  committed: boolean;
  sha: string | null;
  files: string[];
  /** Co-Authored-By trailers removed from the message: the store never carries them. */
  strippedTrailers: number;
  unpushed: number | null;
  hasRemote: boolean;
}

export async function commitStore(store: string, message: string): Promise<CommitResult> {
  return withLock(store, () => {
    ensureStore(store);
    const identity = gitIdentity(store);
    if (!identity) {
      throw new StoreError(
        "git has no committer identity, so the store cannot be committed. Set one:\n" +
          `  git -C ${store} config user.name "Your Name"\n` +
          `  git -C ${store} config user.email "you@example.com"`,
      );
    }

    const lines = message.split("\n");
    const kept = lines.filter((l) => !/^\s*Co-Authored-By\s*:/i.test(l));
    const clean = kept.join("\n").trim();
    if (clean === "") throw new StoreError("A commit needs a message.");

    gitRun(store, ["add", "-A"]);
    const staged = gitTry(store, ["diff", "--cached", "--name-only"]) ?? "";
    if (staged === "") {
      const status = gitStatus(store);
      return { committed: false, sha: null, files: [], strippedTrailers: 0, unpushed: status.unpushed, hasRemote: status.hasRemote };
    }

    gitRun(store, ["commit", "-q", "-m", clean]);
    const status = gitStatus(store);
    return {
      committed: true,
      sha: gitTry(store, ["rev-parse", "--short", "HEAD"]),
      files: staged.split("\n").filter(Boolean),
      strippedTrailers: lines.length - kept.length,
      unpushed: status.unpushed,
      hasRemote: status.hasRemote,
    };
  });
}
