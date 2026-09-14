/** Store provisioning. Port of the bootstrap block in `save-context`'s SKILL.md.
 *
 * The step order is load-bearing and matches the original: directories, then the
 * repo, then the remote and history adoption, and only then the local templates.
 * On a second machine pointed at a hub that already has memory, writing
 * `.gitignore` or `shared/SCHEMA.md` first would leave untracked files in the way
 * of the checkout, and the adoption would fail. */

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { gitIdentity, gitTry, gitRun } from "../git/exec.js";
import { isOwnRepo } from "../git/read.js";
import { sharedDir, type Env } from "./paths.js";

export interface BootstrapReport {
  store: string;
  createdStore: boolean;
  initialisedRepo: boolean;
  /** Remote URL wired during this call, if any. */
  wiredRemote: string | null;
  /** True when the store adopted an existing history from the hub. */
  adoptedHistory: boolean;
  /** Set when the hub has memory this store could not take on, and why. */
  adoptionBlocked: string | null;
  wroteTemplates: string[];
  /** Null when git has no committer identity — commits will fail until it does. */
  identity: { name: string; email: string } | null;
}

/** The SCHEMA contract, copied from `templates/SCHEMA.md` at build time. */
function schemaTemplate(): string | null {
  const here = path.dirname(fileURLToPath(import.meta.url));
  const candidates = [
    path.resolve(here, "..", "..", "..", "assets", "SCHEMA.md"), // packaged
    path.resolve(here, "..", "..", "..", "..", "templates", "SCHEMA.md"), // repo, unbuilt
  ];
  for (const candidate of candidates) {
    try {
      return fs.readFileSync(candidate, "utf8");
    } catch {
      continue;
    }
  }
  return null;
}

export function ensureStore(store: string, env: Env = process.env): BootstrapReport {
  const createdStore = !fs.existsSync(store);
  for (const sub of ["projects", "memories", "shared"]) {
    fs.mkdirSync(path.join(store, sub), { recursive: true });
  }

  const initialisedRepo = !isOwnRepo(store);
  if (initialisedRepo) gitRun(store, ["init", "-q", "-b", "main"]);

  let wiredRemote: string | null = null;
  let adoptedHistory = false;
  let adoptionBlocked: string | null = null;
  const remote = env.MNEMO_REMOTE;
  if (remote && gitTry(store, ["remote", "get-url", "origin"]) === null) {
    gitRun(store, ["remote", "add", "origin", remote]);
    wiredRemote = remote;
    // Adoption is best-effort: an unreachable hub must not block a local store.
    const fetched = gitTry(store, ["fetch", "-q", "origin"]) !== null;
    const hasRemoteMain = fetched && gitTry(store, ["rev-parse", "-q", "--verify", "origin/main"]) !== null;
    const hasLocalHead = gitTry(store, ["rev-parse", "-q", "--verify", "HEAD"]) !== null;
    if (hasRemoteMain && !hasLocalHead) {
      try {
        gitRun(store, ["checkout", "-q", "-B", "main", "--track", "origin/main"]);
        adoptedHistory = true;
      } catch (err) {
        // Adoption is for a virgin store. If this one already holds local files,
        // the hub's history and this store's content are two different memories
        // and merging them is a decision for the user, not a side effect of a
        // save. Wiring the remote still stands; the store stays usable.
        adoptionBlocked =
          `The hub at ${remote} already has memory, but this store has local content that would be overwritten, ` +
          "so the two were not merged. Reconcile them by hand — e.g. move this store aside and clone the hub, or " +
          `push this one over the hub if it is the copy you want to keep. (git: ${err instanceof Error ? err.message : String(err)})`;
      }
    }
  }

  const wroteTemplates: string[] = [];
  const gitignore = path.join(store, ".gitignore");
  if (!fs.existsSync(gitignore)) {
    fs.writeFileSync(gitignore, ".DS_Store\n");
    wroteTemplates.push(".gitignore");
  }
  const schemaPath = path.join(sharedDir(store), "SCHEMA.md");
  if (!fs.existsSync(schemaPath)) {
    const schema = schemaTemplate();
    if (schema !== null) {
      fs.writeFileSync(schemaPath, schema);
      wroteTemplates.push("shared/SCHEMA.md");
    }
  }

  return {
    store,
    createdStore,
    initialisedRepo,
    wiredRemote,
    adoptedHistory,
    adoptionBlocked,
    wroteTemplates,
    identity: gitIdentity(store),
  };
}
