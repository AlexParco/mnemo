/** Frontmatter parsing and *surgical* editing.
 *
 * Two contracts live here:
 *  - `parseFrontmatter` mirrors card.py's parser exactly (card parity).
 *  - `parseDocument`/`renderDocument` round-trip byte-for-byte, and `setField`
 *    rewrites a single key's line while leaving every other byte alone. That is
 *    what lets rename/forget touch `projects:` without reflowing the note, as
 *    the skills require ("do not touch the body prose"). */

import { splitLines, stripQuotes } from "./text.js";

export type FmValue = string | string[];
export type Frontmatter = Record<string, FmValue>;

export interface MnemoDoc {
  /** False when the file has no `---` frontmatter block. */
  hasFrontmatter: boolean;
  /** Raw text between the opening `---` and the closing `\n---`, markers excluded. */
  fmText: string;
  /** Everything from the closing marker onward, i.e. `text.slice(end + 4)`. */
  body: string;
  values: Frontmatter;
}

const KEY_LINE = /^([A-Za-z_][A-Za-z0-9_-]*):\s*(.*)$/;
const LIST_VALUE = /^\[(.*)\]$/;

/** Parse the frontmatter into values. Port of card.py's `parse_frontmatter`. */
export function parseFrontmatter(text: string): Frontmatter {
  const fm: Frontmatter = {};
  if (!text.startsWith("---")) return fm;
  const end = text.indexOf("\n---", 3);
  if (end === -1) return fm;
  for (const raw of splitLines(text.slice(3, end))) {
    const m = KEY_LINE.exec(raw.trim());
    if (!m) continue;
    const key = m[1]!;
    const val = m[2]!.trim();
    const list = LIST_VALUE.exec(val);
    if (list) {
      // Python strips whitespace first, then quotes — and does not re-strip.
      const items = list[1]!.split(",").map((x) => stripQuotes(x.trim()));
      fm[key] = items.filter((x) => x !== "");
    } else {
      fm[key] = stripQuotes(val);
    }
  }
  return fm;
}

export function parseDocument(text: string): MnemoDoc {
  if (!text.startsWith("---")) {
    return { hasFrontmatter: false, fmText: "", body: text, values: {} };
  }
  const end = text.indexOf("\n---", 3);
  if (end === -1) {
    return { hasFrontmatter: false, fmText: "", body: text, values: {} };
  }
  return {
    hasFrontmatter: true,
    fmText: text.slice(3, end),
    body: text.slice(end + 4),
    values: parseFrontmatter(text),
  };
}

export function renderDocument(doc: MnemoDoc): string {
  if (!doc.hasFrontmatter) return doc.body;
  return `---${doc.fmText}\n---${doc.body}`;
}

export function formatFmValue(value: FmValue): string {
  return Array.isArray(value) ? `[${value.join(", ")}]` : value;
}

/** Read a key as a list, applying card.py's `project_slugs` rule: a scalar
 * becomes a one-element list, a missing key an empty one. */
export function asList(fm: Frontmatter, key: string): string[] {
  const v = fm[key];
  if (v === undefined) return [];
  return Array.isArray(v) ? v : [v];
}

/** Replace one key's line in the frontmatter, preserving indentation, line
 * endings and every other line. Appends the key if absent. */
export function setField(doc: MnemoDoc, key: string, value: FmValue): MnemoDoc {
  const rendered = formatFmValue(value);
  if (!doc.hasFrontmatter) {
    const fmText = `\n${key}: ${rendered}`;
    return { hasFrontmatter: true, fmText, body: doc.body, values: { [key]: value } };
  }
  const escaped = key.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp(`^([ \\t]*)${escaped}[ \\t]*:[ \\t]*.*$`, "m");
  const fmText = re.test(doc.fmText)
    ? doc.fmText.replace(re, (_m, indent: string) => `${indent}${key}: ${rendered}`)
    : `${doc.fmText}\n${key}: ${rendered}`;
  return { ...doc, fmText, values: { ...doc.values, [key]: value } };
}
