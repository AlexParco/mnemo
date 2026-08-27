/** The "resume card": a deterministic render of a project's state.
 *
 * This is a port of `skills/load-context/card.py`, and its output is a contract:
 * the parity test asserts byte-for-byte equality against the Python original for
 * every fixture project in both languages. Read that script before changing
 * anything here — the quirks below are deliberate, not accidents.
 *
 * One intentional divergence: card.py keeps the core section names in Python
 * `set`s, so with both language variants present in one `pending.md` its item
 * order varies between processes. Here the order is fixed. */

import fs from "node:fs";
import { parseFrontmatter, type Frontmatter } from "../store/frontmatter.js";
import { loadMemories, memoriesForProject, type Memory } from "../store/memory.js";
import { indexPath, machineLabel, pendingPath } from "../store/paths.js";
import { IN_PROGRESS, NEXT, CORE_SECTIONS, machineFlag, openItems, parsePending } from "../store/pending.js";

const MAX_PENDING = 5;

const TYPE_ICON: Record<string, string> = {
  decision: "⚖",
  constraint: "⛔",
  gotcha: "⚠",
  bug: "🐛",
  reference: "🔗",
  todo: "☐",
};

/** Prefix → icon for non-core `pending.md` sections. Order matters: the first
 * prefix that matches wins, mirroring the Python dict's insertion order. */
const SECTION_ICONS: ReadonlyArray<readonly [string, string]> = [
  ["bloqueado", "⛔"], ["blocked", "⛔"],
  ["deuda", "🧾"], ["debt", "🧾"],
  ["desplegado", "✅"], ["deployed", "✅"], ["hecho", "✅"], ["done", "✅"],
  ["ramas", "🌿"], ["branches", "🌿"], ["pusheado", "🌿"], ["pushed", "🌿"],
  ["riesgos", "⚠"], ["risks", "⚠"],
];

export type Lang = "en" | "es";

interface Labels {
  services: string;
  resume: string;
  pending: string;
  none: string;
  more: string;
  saved: string;
  noNotes: string;
  noSummary: string;
  status: Record<string, string>;
  start: string;
  one: string;
  many: readonly [string, string];
}

const LABELS: Record<Lang, Labels> = {
  en: {
    services: "Services", resume: "Resume with", pending: "Pending",
    none: "(none)", more: "more", saved: "Saved context",
    noNotes: "(no notes)", noSummary: "(no summary)",
    status: { active: "active", paused: "paused", done: "done" },
    start: "Where do we start?", one: "Continue with 1?",
    many: ["Continue with ", " or "],
  },
  es: {
    services: "Servicios", resume: "Retomar por", pending: "Pendientes",
    none: "(ninguno)", more: "más", saved: "Contexto guardado",
    noNotes: "(sin notas)", noSummary: "(sin resumen)",
    status: { active: "activo", paused: "pausado", done: "hecho" },
    start: "¿Por dónde arrancamos?", one: "¿Seguimos con la 1?",
    many: ["¿Seguimos con ", " o "],
  },
};

export class ProjectNotFoundError extends Error {
  constructor(readonly slug: string, readonly store: string) {
    super(`project '${slug}' not found in ${store}`);
    this.name = "ProjectNotFoundError";
  }
}

/** `argv`/`$MNEMO_LANG` → a supported language, defaulting to English. */
export function resolveLang(value: string | undefined, env: NodeJS.ProcessEnv = process.env): Lang {
  const raw = value !== undefined ? value : (env.MNEMO_LANG ?? "en");
  const lang = raw.trim().toLowerCase().slice(0, 2);
  return lang === "en" || lang === "es" ? lang : "en";
}

function sectionIcon(key: string): string {
  for (const [prefix, icon] of SECTION_ICONS) {
    if (key.startsWith(prefix)) return icon;
  }
  return "▸";
}

/** `dict.get(key, fallback)`: a present-but-empty value is returned as-is,
 * unlike the normalised accessors in `store/project.ts`. */
function fmGet(fm: Frontmatter, key: string, fallback: string): string {
  const v = fm[key];
  if (v === undefined) return fallback;
  return Array.isArray(v) ? v.join(", ") : v;
}

export interface CardOptions {
  lang?: Lang;
  machine?: string;
  /** Pre-loaded memories, to avoid re-reading the store. */
  memories?: Memory[];
}

/** Render the card. Throws `ProjectNotFoundError` when the project has no
 * INDEX.md. The returned string has no trailing newline. */
export function renderCard(store: string, slug: string, options: CardOptions = {}): string {
  let indexRaw: string;
  try {
    indexRaw = fs.readFileSync(indexPath(store, slug), "utf8");
  } catch {
    throw new ProjectNotFoundError(slug, store);
  }

  const L = LABELS[options.lang ?? "en"];
  const here = options.machine ?? machineLabel();

  const fm = parseFrontmatter(indexRaw);
  const name = fmGet(fm, "name", slug);
  const rawStatus = fmGet(fm, "status", "?");
  const status = L.status[rawStatus] ?? rawStatus;
  const servicesValue = fm["services"];
  const services = servicesValue === undefined ? [] : Array.isArray(servicesValue) ? servicesValue : [servicesValue];

  let pendingRaw = "";
  try {
    pendingRaw = fs.readFileSync(pendingPath(store, slug), "utf8");
  } catch {
    // no pending.md is a valid state: the card just shows "(none)"
  }
  const sections = parsePending(pendingRaw);
  const inProgress = openItems(sections, IN_PROGRESS);
  const next = openItems(sections, NEXT);
  const pend = [...inProgress, ...next];

  const memories = memoriesForProject(options.memories ?? loadMemories(store), slug);

  const out: string[] = [];
  out.push(`📁 ${slug} · ${status}`);
  out.push(`   ${name}`);
  if (services.length > 0) {
    const shown = services.slice(0, 2).join(", ");
    const extra = services.length > 2 ? ` (+${services.length - 2})` : "";
    out.push(`   ${L.services}: ${shown}${extra}`);
  }

  const resume = inProgress.length > 0 ? inProgress.slice(0, 2) : next.slice(0, 1);
  out.push("");
  out.push(`▶ ${L.resume}: ${resume.length > 0 ? resume.join("; ") : "—"}`);

  out.push("");
  out.push(L.pending);
  if (pend.length > 0) {
    pend.slice(0, MAX_PENDING).forEach((it, i) => {
      out.push(` ${i + 1}. ${machineFlag(it, here)}${it}`);
    });
    if (pend.length > MAX_PENDING) {
      out.push(`    … (+${pend.length - MAX_PENDING} ${L.more})`);
    }
  } else {
    out.push(` ${L.none}`);
  }

  // Free-form sections (blocked, debt, deployed, …) render as their own blocks,
  // so the card adapts to whatever a project actually tracks.
  for (const section of sections) {
    if (CORE_SECTIONS.has(section.key) || section.items.length === 0) continue;
    out.push("");
    out.push(`${sectionIcon(section.key)} ${section.label} (${section.items.length})`);
    for (const item of section.items) {
      const mark = item.done ? "✓ " : "";
      out.push(` • ${machineFlag(item.text, here)}${mark}${item.text}`);
    }
  }

  out.push("");
  out.push(`${L.saved} (${memories.length})`);
  if (memories.length > 0) {
    for (const m of memories) {
      out.push(` ${TYPE_ICON[m.type] ?? "•"} ${m.summary || L.noSummary}`);
    }
  } else {
    out.push(` ${L.noNotes}`);
  }

  const shown = Math.min(pend.length, MAX_PENDING);
  let prompt: string;
  if (shown === 0) {
    prompt = L.start;
  } else if (shown === 1) {
    prompt = L.one;
  } else {
    const nums = Array.from({ length: shown }, (_, i) => String(i + 1));
    const [head, joiner] = L.many;
    prompt = head + nums.slice(0, -1).join(", ") + joiner + nums[nums.length - 1]! + "?";
  }
  out.push("");
  out.push(prompt);

  return out.join("\n");
}
