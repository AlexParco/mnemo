/** Syncing with the hub: pull, conflict handling, push.
 *
 * Conflict *resolution* stays with the model — merging two memories is reading,
 * not pattern matching — but the git mechanics do not. In particular the
 * `GIT_EDITOR=true` that the skills repeat in three files, with a warning that
 * without it "the shell hangs in the editor", is handled in `gitAttempt`. */

import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { gitAttempt, gitTry } from "./exec.js";
import { gitStatus } from "./read.js";
import { scanRange, pushBase, type SecretFinding } from "./secrets.js";
import { withLock } from "../store/lock.js";
import { StoreError } from "../store/write.js";

export interface Conflict {
  file: string;
  /** The file as git left it, conflict markers included. */
  content: string;
}

export interface SyncResult {
  hasRemote: boolean;
  /** True when the pull ran and left the store clean. */
  pulled: boolean;
  conflicts: Conflict[];
  rebaseInProgress: boolean;
  detail: string;
}

export function conflictedFiles(store: string): string[] {
  const out = gitTry(store, ["diff", "--name-only", "--diff-filter=U"]);
  return out ? out.split("\n").filter(Boolean) : [];
}

function readConflicts(store: string): Conflict[] {
  return conflictedFiles(store).map((file) => ({
    file,
    content: fs.readFileSync(path.join(store, file), "utf8"),
  }));
}

export async function syncStore(store: string): Promise<SyncResult> {
  return withLock(store, () => {
    const before = gitStatus(store);
    if (!before.hasRemote) {
      return { hasRemote: false, pulled: false, conflicts: [], rebaseInProgress: false, detail: "This store has no remote; there is nothing to sync with." };
    }
    if (before.rebaseInProgress) {
      return {
        hasRemote: true, pulled: false, conflicts: readConflicts(store), rebaseInProgress: true,
        detail: "A rebase was already in progress — finish it before syncing again.",
      };
    }
    // Without a tracking branch there is nothing to rebase onto, and guessing a
    // branch could merge two unrelated memories. The first push sets tracking up.
    if (gitTry(store, ["rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"]) === null) {
      return {
        hasRemote: true, pulled: false, conflicts: [], rebaseInProgress: false,
        detail: "The remote is wired but this branch tracks nothing yet; the first push will set that up.",
      };
    }

    const pull = gitAttempt(store, ["pull", "--rebase", "--autostash"]);
    const conflicts = readConflicts(store);
    const after = gitStatus(store);
    if (pull.ok && conflicts.length === 0) {
      return { hasRemote: true, pulled: true, conflicts: [], rebaseInProgress: false, detail: pull.stdout || "Already up to date." };
    }
    return {
      hasRemote: true, pulled: false, conflicts, rebaseInProgress: after.rebaseInProgress,
      detail: pull.stderr || pull.stdout || "The pull did not complete.",
    };
  });
}

/** Write a resolved file and stage it. Refuses content that still carries
 * conflict markers: the skills make "no `<<<<<<<` may remain" a closing check,
 * and a half-merged memory committed as resolved is worse than a conflict. */
export async function resolveConflict(store: string, file: string, content: string): Promise<{ remaining: string[] }> {
  return withLock(store, () => {
    const target = path.resolve(store, file);
    if (!target.startsWith(path.resolve(store) + path.sep)) {
      throw new StoreError(`'${file}' is outside the store.`);
    }
    if (/^(<<<<<<<|=======|>>>>>>>)/m.test(content)) {
      throw new StoreError(
        `The content for '${file}' still contains conflict markers. Merge the two sides — keeping the information ` +
          "from both — and send the finished file.",
      );
    }
    fs.writeFileSync(target, content.endsWith("\n") ? content : `${content}\n`);
    const add = gitAttempt(store, ["add", "--", file]);
    if (!add.ok) throw new StoreError(`git could not stage '${file}': ${add.stderr}`);
    return { remaining: conflictedFiles(store) };
  });
}

export interface RebaseResult {
  action: "continue" | "abort";
  ok: boolean;
  rebaseInProgress: boolean;
  remaining: string[];
  detail: string;
}

export async function rebaseAction(store: string, action: "continue" | "abort"): Promise<RebaseResult> {
  return withLock(store, () => {
    if (!gitStatus(store).rebaseInProgress) {
      return { action, ok: false, rebaseInProgress: false, remaining: [], detail: "No rebase is in progress." };
    }
    const remaining = conflictedFiles(store);
    if (action === "continue" && remaining.length > 0) {
      return {
        action, ok: false, rebaseInProgress: true, remaining,
        detail: `Still unresolved: ${remaining.join(", ")}. Resolve them before continuing.`,
      };
    }
    const res = gitAttempt(store, ["rebase", `--${action}`]);
    const after = gitStatus(store);
    return {
      action, ok: res.ok, rebaseInProgress: after.rebaseInProgress, remaining: conflictedFiles(store),
      detail: res.stderr || res.stdout || (action === "abort" ? "Rebase aborted; the store is as it was." : "Rebase continued."),
    };
  });
}

export type PushRefusal = "no-remote" | "rebase-in-progress" | "nothing-to-push" | "secrets" | "bad-acknowledgement";

export interface PushResult {
  pushed: boolean;
  refused: PushRefusal | null;
  findings: SecretFinding[];
  /** Present when findings blocked the push: the value that unblocks this exact set. */
  acknowledgeToken: string | null;
  commits: number | null;
  detail: string;
}

/** Bound to HEAD and to the exact findings, so it stops being valid the moment
 * either changes. Stateless on purpose: it survives a server restart and cannot
 * be replayed against different content. */
function acknowledgeToken(store: string, findings: SecretFinding[]): string {
  const head = gitTry(store, ["rev-parse", "HEAD"]) ?? "";
  const key = findings.map((f) => `${f.rule}|${f.file}|${f.line}`).sort().join("\n");
  return createHash("sha256").update(`${head}\n${key}`).digest("hex").slice(0, 12);
}

export async function pushStore(store: string, acknowledge?: string): Promise<PushResult> {
  return withLock(store, () => {
    const none = { pushed: false, findings: [] as SecretFinding[], acknowledgeToken: null, commits: null };
    const status = gitStatus(store);
    if (!status.hasRemote) {
      return { ...none, refused: "no-remote" as const, detail: "This store has no remote, so it lives only on this machine." };
    }
    if (status.rebaseInProgress) {
      return { ...none, refused: "rebase-in-progress" as const, detail: "A rebase is in progress; finish or abort it before pushing." };
    }

    const base = pushBase(store);
    if (base === null) {
      return { ...none, refused: "nothing-to-push" as const, detail: "The store has no commits yet." };
    }
    const countRaw = gitTry(store, ["rev-list", "--count", `${base}..HEAD`]);
    const commits = countRaw === null ? null : Number.parseInt(countRaw, 10);
    if (commits === 0) {
      return { ...none, refused: "nothing-to-push" as const, detail: "Everything is already on the hub." };
    }

    // The scan is not a step that can be skipped: there is no path to the network
    // that does not come through here.
    const findings = scanRange(store);
    if (findings.length > 0) {
      const token = acknowledgeToken(store, findings);
      if (acknowledge !== token) {
        return {
          pushed: false,
          refused: acknowledge ? ("bad-acknowledgement" as const) : ("secrets" as const),
          findings,
          acknowledgeToken: token,
          commits,
          detail: acknowledge
            ? "That acknowledgement does not match these findings — they changed, or it was not the one issued. Nothing was pushed."
            : `${findings.length} possible secret(s) in what would be published. Nothing was pushed.`,
        };
      }
    }

    const upstream = gitTry(store, ["rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"]);
    const branch = status.branch ?? "main";
    const res = upstream
      ? gitAttempt(store, ["push", "-q"])
      : gitAttempt(store, ["push", "-q", "-u", "origin", branch]);
    if (!res.ok) {
      return { ...none, refused: null, findings, commits, detail: `git push failed: ${res.stderr || res.stdout}` };
    }
    return {
      pushed: true, refused: null, findings, acknowledgeToken: null, commits,
      detail: `Pushed ${commits} commit(s) to the hub.`,
    };
  });
}
