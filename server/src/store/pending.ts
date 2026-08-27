/** `pending.md` parsing. Sections are free-form by design (SCHEMA.md): the card
 * renders whichever exist, so this parser keeps every section it finds, in the
 * order it found it, and never assumes a fixed set. */

import { splitLines, truncate } from "./text.js";

export interface PendingItem {
  text: string;
  done: boolean;
}

export interface PendingSection {
  /** Lower-cased title, used for matching against the known section names. */
  key: string;
  /** Title exactly as written, used for display. */
  label: string;
  items: PendingItem[];
}

const HEADING = /^##\s+(.*)$/;
const ITEM = /^\s*-\s*\[( |x|X)\]\s*(.+)$/;

/** Section titles that feed the numbered "Pending" list and "Resume with".
 *
 * card.py holds these in Python `set`s and iterates them, which makes its output
 * order unspecified when a file carries both language variants (string hashing is
 * randomised per process). Here they are ordered lists, so the render is
 * deterministic in every case — including the mixed one. */
export const IN_PROGRESS = ["in progress", "en curso"];
export const NEXT = ["next", "siguiente"];
export const CORE_SECTIONS = new Set([...IN_PROGRESS, ...NEXT]);

export function parsePending(text: string): PendingSection[] {
  const sections = new Map<string, PendingSection>();
  let current: PendingSection | undefined;
  for (const line of splitLines(text)) {
    const h = HEADING.exec(line.trim());
    if (h) {
      const label = h[1]!.trim();
      const key = label.toLowerCase();
      let section = sections.get(key);
      if (!section) {
        section = { key, label, items: [] };
        sections.set(key, section);
      }
      current = section;
      continue;
    }
    const item = ITEM.exec(line);
    if (item && current) {
      current.items.push({ text: truncate(item[2]!), done: item[1]!.toLowerCase() === "x" });
    }
  }
  return [...sections.values()];
}

export function sectionByKey(sections: PendingSection[], key: string): PendingSection | undefined {
  return sections.find((s) => s.key === key);
}

/** Open (unchecked) item texts across the given section keys, in key order. */
export function openItems(sections: PendingSection[], keys: readonly string[]): string[] {
  const out: string[] = [];
  for (const key of keys) {
    const section = sectionByKey(sections, key);
    if (!section) continue;
    out.push(...section.items.filter((i) => !i.done).map((i) => i.text));
  }
  return out;
}

/** `"⚠ "` when the item is stamped for a machine other than this one. */
export function machineFlag(text: string, here: string): string {
  const m = /\[@([^\]]+)\]/.exec(text);
  return m && m[1]!.trim() !== here ? "⚠ " : "";
}
