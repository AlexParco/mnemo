/** Projects: `projects/<slug>/INDEX.md` + `pending.md`. */

import fs from "node:fs";
import { parseDocument, asList, type Frontmatter } from "./frontmatter.js";
import { indexPath, pendingPath, projectsDir } from "./paths.js";
import { parsePending, type PendingSection } from "./pending.js";
import { compareCodePoints } from "./text.js";

export interface Project {
  slug: string;
  name: string;
  status: string;
  services: string[];
  updated: string;
  /** INDEX.md body, without the frontmatter. */
  description: string;
  indexRaw: string;
  fm: Frontmatter;
  pending: PendingSection[];
  /** False when `pending.md` is absent — a valid state, not an error. */
  hasPending: boolean;
}

/** Project slugs = directories under `projects/`, in code-point order. */
export function projectSlugs(store: string): string[] {
  let entries: fs.Dirent[];
  try {
    entries = fs.readdirSync(projectsDir(store), { withFileTypes: true });
  } catch {
    return [];
  }
  return entries
    .filter((e) => e.isDirectory())
    .map((e) => e.name)
    .sort(compareCodePoints);
}

export function projectExists(store: string, slug: string): boolean {
  return fs.existsSync(indexPath(store, slug));
}

/** Read a project, or `null` when it has no `INDEX.md` — the same existence
 * test the skills and the card use. */
export function readProject(store: string, slug: string): Project | null {
  let indexRaw: string;
  try {
    indexRaw = fs.readFileSync(indexPath(store, slug), "utf8");
  } catch {
    return null;
  }
  const doc = parseDocument(indexRaw);
  const fm = doc.values;
  const str = (key: string, fallback: string) => {
    const v = fm[key];
    return typeof v === "string" && v !== "" ? v : fallback;
  };

  let pendingRaw = "";
  let hasPending = true;
  try {
    pendingRaw = fs.readFileSync(pendingPath(store, slug), "utf8");
  } catch {
    hasPending = false;
  }

  return {
    slug,
    name: str("name", slug),
    status: str("status", "?"),
    services: asList(fm, "services"),
    updated: str("updated", ""),
    description: doc.body.trim(),
    indexRaw,
    fm,
    pending: parsePending(pendingRaw),
    hasPending,
  };
}
