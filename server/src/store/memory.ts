/** Atomic memories: `memories/<id>.md`, one fact per file, multi-tagged.
 *
 * Project filtering reads the parsed `projects:` field — never a grep of the bare
 * slug. That is the fix for the false positive the skills warn about twice: a
 * short slug matches inside a longer one (`mnemo` inside `mnemo-web`) and also
 * matches prose in the body. */

import fs from "node:fs";
import path from "node:path";
import { asList, parseDocument, type Frontmatter } from "./frontmatter.js";
import { memoriesDir } from "./paths.js";
import { compareCodePoints, splitLines, truncate } from "./text.js";

/** Render/sort order for memory types, from SCHEMA.md. */
export const TYPE_ORDER = ["decision", "constraint", "gotcha", "bug", "reference", "todo"] as const;

export interface Memory {
  /** File name without `.md`. SCHEMA.md makes this the authoritative id. */
  id: string;
  path: string;
  /** `id` as declared in the frontmatter; SCHEMA.md requires it to equal `id`. */
  declaredId: string;
  type: string;
  projects: string[];
  services: string[];
  tags: string[];
  updated: string;
  author: string;
  /** First content line of the body, truncated — what the card shows. */
  summary: string;
  body: string;
  raw: string;
  fm: Frontmatter;
}

const MD_MARKERS = /^[#\-*>\s]+/;

/** First non-empty body line, stripped of leading markdown markers. Port of
 * card.py's `memory_summary`; `""` when the body has no content. */
export function summaryFromBody(body: string): string {
  for (const line of splitLines(body)) {
    const s = line.trim().replace(MD_MARKERS, "").trim();
    if (s) return truncate(s);
  }
  return "";
}

export function parseMemory(raw: string, file: string): Memory {
  const doc = parseDocument(raw);
  const fm = doc.values;
  const str = (key: string) => {
    const v = fm[key];
    return typeof v === "string" ? v : "";
  };
  return {
    id: path.basename(file, ".md"),
    path: file,
    declaredId: str("id"),
    type: str("type"),
    projects: asList(fm, "projects"),
    services: asList(fm, "services"),
    tags: asList(fm, "tags"),
    updated: str("updated"),
    author: str("author"),
    summary: summaryFromBody(doc.body),
    body: doc.body,
    raw,
    fm,
  };
}

/** `.md` files in `memories/`, in code-point order. Dotfiles are included:
 * `pathlib.Path.glob("*.md")` includes them, and the card counts them. */
export function memoryFiles(store: string): string[] {
  const dir = memoriesDir(store);
  let names: string[];
  try {
    names = fs.readdirSync(dir);
  } catch {
    return [];
  }
  return names
    .filter((n) => n.endsWith(".md"))
    .sort(compareCodePoints)
    .map((n) => path.join(dir, n));
}

export function loadMemories(store: string): Memory[] {
  const out: Memory[] = [];
  for (const file of memoryFiles(store)) {
    let raw: string;
    try {
      raw = fs.readFileSync(file, "utf8");
    } catch {
      continue;
    }
    out.push(parseMemory(raw, file));
  }
  return out;
}

export function typeRank(type: string): number {
  const i = (TYPE_ORDER as readonly string[]).indexOf(type);
  return i === -1 ? TYPE_ORDER.length : i;
}

/** Memories tagged with `slug`, ordered by type then file name. The sort is
 * stable, so same-type memories keep their file-name order. */
export function memoriesForProject(memories: Memory[], slug: string): Memory[] {
  return memories.filter((m) => m.projects.includes(slug)).sort((a, b) => typeRank(a.type) - typeRank(b.type));
}
