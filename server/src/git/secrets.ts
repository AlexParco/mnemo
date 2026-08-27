/** Secret scan. Nothing leaves the machine without passing this.
 *
 * In the plugin this is a `grep` the model is told to run before every push
 * (`save-context` step 7a) — a step that can be skipped, and a skipped scan looks
 * exactly like a clean one. Here it is not a step: `push()` cannot reach the
 * network without going through `scanRange`.
 *
 * Only ADDED lines are scanned. With the upstream as the base, those are exactly
 * what this push would publish; removed and context lines are already on the hub,
 * and flagging them would block every future push over an old leak. */

import { gitTry } from "./exec.js";

/** git's constant for the empty tree — the base when nothing has been pushed yet,
 * so a first push scans the whole history as additions. */
const EMPTY_TREE = "4b825dc642cb6eb9a060e54bf8d69288fbee4904";

export interface SecretRule {
  name: string;
  re: RegExp;
}

/** The plugin's patterns, plus one refinement.
 *
 * `credential assignment` requires the value to be an unbroken 12+ character
 * token. The shell version matches `token:` followed by anything, which in a
 * store made of engineering prose fires on ordinary notes ("token: rotate every
 * 15 minutes") — and a false positive here is a hard block on syncing memory. */
export const RULES: SecretRule[] = [
  { name: "private key block", re: /-----BEGIN [A-Z ]*PRIVATE KEY-----/i },
  { name: "AWS access key id", re: /\bAKIA[0-9A-Z]{16}\b/ },
  { name: "GitHub token", re: /\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}\b/i },
  { name: "GitHub fine-grained token", re: /\bgithub_pat_[A-Za-z0-9_]{20,}\b/i },
  { name: "Slack token", re: /\bxox[baprs]-[A-Za-z0-9-]{8,}/i },
  { name: "URL with embedded credentials", re: /\b[a-z][a-z0-9+.-]*:\/\/[^/:@\s]+:[^/@\s]+@/i },
  {
    name: "credential assignment",
    re: /\b(?:password|passwd|secret|token|api[-_]?key)["'\s]*[:=]["'\s]*([A-Za-z0-9+/_.-]{12,})/i,
  },
];

export interface SecretFinding {
  rule: string;
  file: string;
  /** Line number in the file as it would be pushed. */
  line: number;
  /** The offending line with the match masked: enough to find it, not to leak it. */
  excerpt: string;
}

/** Keep the shape, drop the payload. The finding travels through an agent
 * transcript, so echoing the secret verbatim would be its own leak. */
function mask(line: string, match: string): string {
  const shown = match.length > 8 ? `${match.slice(0, 3)}…${match.slice(-2)}` : "…";
  return line.replace(match, `[redacted ${match.length} chars: ${shown}]`).trim().slice(0, 200);
}

const FILE_HEADER = /^\+\+\+ b\/(.*)$/;
const HUNK_HEADER = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

/** Scan a unified diff for secrets in added lines. */
export function scanDiff(diff: string): SecretFinding[] {
  const findings: SecretFinding[] = [];
  let file = "?";
  let lineNo = 0;
  for (const raw of diff.split("\n")) {
    const header = FILE_HEADER.exec(raw);
    if (header) {
      file = header[1]!;
      continue;
    }
    const hunk = HUNK_HEADER.exec(raw);
    if (hunk) {
      lineNo = Number.parseInt(hunk[1]!, 10);
      continue;
    }
    if (raw.startsWith("---") || raw.startsWith("+++")) continue;
    if (raw.startsWith("+")) {
      const content = raw.slice(1);
      for (const rule of RULES) {
        const m = rule.re.exec(content);
        if (m) findings.push({ rule: rule.name, file, line: lineNo, excerpt: mask(content, m[0]) });
      }
      lineNo++;
    } else if (!raw.startsWith("-")) {
      lineNo++; // context line: advances the new-file counter
    }
  }
  return findings;
}

/** What this store would publish: the upstream branch if it has one, else
 * `origin/main`, else everything. Null when there is nothing to push. */
export function pushBase(store: string): string | null {
  if (gitTry(store, ["rev-parse", "-q", "--verify", "HEAD"]) === null) return null;
  const upstream = gitTry(store, ["rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"]);
  if (upstream && gitTry(store, ["rev-parse", "-q", "--verify", upstream]) !== null) return upstream;
  if (gitTry(store, ["rev-parse", "-q", "--verify", "origin/main"]) !== null) return "origin/main";
  return EMPTY_TREE;
}

/** Scan everything a push would publish. */
export function scanRange(store: string): SecretFinding[] {
  const base = pushBase(store);
  if (base === null) return [];
  const diff = gitTry(store, ["diff", "--no-color", "--no-ext-diff", base, "HEAD"]);
  return diff ? scanDiff(diff) : [];
}
