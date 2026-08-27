/** Read-only git inspection of the store.
 *
 * Nothing here mutates: sync, commit and push land in P3 behind the lock. These
 * calls only answer "where does this store stand?" so an agent can tell the user
 * whether their memory is actually on the hub yet. */

import fs from "node:fs";
import { gitTry } from "./exec.js";

export interface GitStatus {
  isRepo: boolean;
  hasRemote: boolean;
  remoteUrl: string | null;
  branch: string | null;
  /** Uncommitted changes in the worktree. */
  dirty: boolean;
  /** Commits not yet on the upstream branch; null when there is no upstream. */
  unpushed: number | null;
  rebaseInProgress: boolean;
}

function gitPathExists(store: string, name: string): boolean {
  const p = gitTry(store, ["rev-parse", "--git-path", name]);
  if (!p) return false;
  // `--git-path` returns a path relative to the store unless it is absolute.
  return fs.existsSync(p.startsWith("/") ? p : `${store}/${p}`);
}

export function gitStatus(store: string): GitStatus {
  const isRepo = gitTry(store, ["rev-parse", "--git-dir"]) !== null;
  if (!isRepo) {
    return { isRepo: false, hasRemote: false, remoteUrl: null, branch: null, dirty: false, unpushed: null, rebaseInProgress: false };
  }
  const remoteUrl = gitTry(store, ["remote", "get-url", "origin"]);
  // On a store with no commits yet HEAD is unborn, and `rev-parse` fails —
  // which is exactly the state right after bootstrap. `symbolic-ref` answers.
  const branch = gitTry(store, ["symbolic-ref", "--short", "HEAD"]) ?? gitTry(store, ["rev-parse", "--abbrev-ref", "HEAD"]);
  const porcelain = gitTry(store, ["status", "--porcelain"]);
  // Prefer the tracked upstream; fall back to origin/main for stores wired by
  // hand, which the README documents as a supported path.
  const count = gitTry(store, ["rev-list", "--count", "@{u}..HEAD"]) ?? gitTry(store, ["rev-list", "--count", "origin/main..HEAD"]);
  const unpushed = count === null ? null : Number.parseInt(count, 10);

  return {
    isRepo: true,
    hasRemote: remoteUrl !== null,
    remoteUrl,
    branch,
    dirty: porcelain !== null && porcelain !== "",
    unpushed: Number.isFinite(unpushed as number) ? (unpushed as number) : null,
    rebaseInProgress: gitPathExists(store, "rebase-merge") || gitPathExists(store, "rebase-apply"),
  };
}
