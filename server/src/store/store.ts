/** Aggregation over the store: the shapes the read tools hand back. */

import fs from "node:fs";
import path from "node:path";
import { renderCard, type Lang } from "../card/render.js";
import { loadMemories, memoriesForProject, type Memory } from "./memory.js";
import { machineLabel, sharedDir } from "./paths.js";
import { projectSlugs, readProject, type Project } from "./project.js";
import { compareCodePoints } from "./text.js";

/** Sort order for the overview: active work first. Anything unrecognised sorts last. */
const STATUS_ORDER = ["active", "paused", "done"];

export interface ProjectSummary {
  slug: string;
  name: string;
  status: string;
  services: string[];
  updated: string;
  /** Memories tagged with this project. */
  memories: number;
  /** Of those, how many are tagged with more than one project (the overlap). */
  shared: number;
}

export interface Overview {
  store: string;
  exists: boolean;
  projects: ProjectSummary[];
  totals: {
    projects: number;
    memories: number;
    /** Files under `shared/` — global conventions, not per-project memories. */
    sharedFiles: number;
    /** Memories tagged only with projects that have no `projects/<slug>/`. */
    orphanMemories: number;
  };
}

function statusRank(status: string): number {
  const i = STATUS_ORDER.indexOf(status);
  return i === -1 ? STATUS_ORDER.length : i;
}

export function sharedFiles(store: string): string[] {
  try {
    return fs.readdirSync(sharedDir(store)).sort(compareCodePoints);
  } catch {
    return [];
  }
}

export function overview(store: string): Overview {
  const slugs = projectSlugs(store);
  const memories = loadMemories(store);
  const known = new Set(slugs);

  const projects: ProjectSummary[] = [];
  for (const slug of slugs) {
    const project = readProject(store, slug);
    if (!project) continue; // a directory without INDEX.md is not a project
    const tagged = memories.filter((m) => m.projects.includes(slug));
    projects.push({
      slug,
      name: project.name,
      status: project.status,
      services: project.services,
      updated: project.updated,
      memories: tagged.length,
      shared: tagged.filter((m) => m.projects.length > 1).length,
    });
  }

  projects.sort((a, b) => statusRank(a.status) - statusRank(b.status) || compareCodePoints(b.updated, a.updated));

  return {
    store,
    exists: fs.existsSync(store),
    projects,
    totals: {
      projects: projects.length,
      memories: memories.length,
      sharedFiles: sharedFiles(store).length,
      orphanMemories: memories.filter((m) => m.projects.length > 0 && !m.projects.some((p) => known.has(p))).length,
    },
  };
}

export interface ProjectContext {
  card: string;
  project: Project;
  memories: Memory[];
  shared: Array<{ name: string; content: string }>;
  machine: string;
}

/** Everything `load-context` reads: the card, the project, its memories
 * (overlap included) and the global conventions in `shared/`. */
export function loadProjectContext(store: string, slug: string, lang: Lang): ProjectContext | null {
  const project = readProject(store, slug);
  if (!project) return null;
  const machine = machineLabel();
  const all = loadMemories(store);
  const shared = sharedFiles(store).map((name) => ({
    name,
    content: fs.readFileSync(path.join(sharedDir(store), name), "utf8"),
  }));
  return {
    card: renderCard(store, slug, { lang, machine, memories: all }),
    project,
    memories: memoriesForProject(all, slug),
    shared,
    machine,
  };
}

export interface SearchOptions {
  query?: string;
  project?: string;
  type?: string;
  limit?: number;
}

/** Free-text search over memories: case-insensitive, across id, tags, services,
 * projects and the whole body.
 *
 * The query is split into terms and every term must appear. A plain substring
 * match would miss the most common query shape there is — ids are kebab-case
 * (`orion-rate-limit-invariant`) while people and agents type spaces
 * ("rate limit"). */
export function searchMemories(store: string, options: SearchOptions): Memory[] {
  const { query, project, type, limit = 25 } = options;
  const terms = (query ?? "").toLowerCase().split(/\s+/).filter((t) => t !== "");
  let found = loadMemories(store);
  if (project) found = found.filter((m) => m.projects.includes(project));
  if (type) found = found.filter((m) => m.type === type);
  if (terms.length > 0) {
    found = found.filter((m) => {
      const haystack = [m.id, m.type, m.body, m.tags.join(" "), m.services.join(" "), m.projects.join(" ")]
        .join("\n")
        .toLowerCase();
      return terms.every((t) => haystack.includes(t));
    });
  }
  return found.slice(0, limit);
}
