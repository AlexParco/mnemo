/** Advisory lock over a store.
 *
 * New requirement in the MCP world: the skills drive git through one model, one
 * step at a time, so writes were implicitly serialised. A server can be called by
 * several agents at once, so every mutation takes this lock first.
 *
 * The lock lives in the state dir, not in the store, so it never shows up in
 * `git status` and works even against a read-only checkout. */

import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import type { Env } from "./paths.js";

const STALE_MS = 60_000;
const POLL_MS = 50;
const DEFAULT_TIMEOUT_MS = 10_000;

export class LockTimeoutError extends Error {
  constructor(readonly lockFile: string) {
    super(`could not acquire the mnemo store lock at ${lockFile}`);
    this.name = "LockTimeoutError";
  }
}

function lockDir(env: Env = process.env): string {
  const base = env.XDG_STATE_HOME || path.join(os.homedir(), ".local", "state");
  return path.join(base, "mnemo", "locks");
}

/** One lock file per store, keyed by its real path so two aliases of the same
 * store (symlink, relative path) share a lock. */
export function lockFileFor(store: string, env: Env = process.env): string {
  let key = store;
  try {
    key = fs.realpathSync(store);
  } catch {
    key = path.resolve(store);
  }
  const safe = key.replace(/[^A-Za-z0-9]+/g, "_").replace(/^_+|_+$/g, "");
  return path.join(lockDir(env), `${safe}.lock`);
}

function holderIsGone(file: string): boolean {
  let raw: string;
  let stat: fs.Stats;
  try {
    stat = fs.statSync(file);
    raw = fs.readFileSync(file, "utf8");
  } catch {
    return true; // vanished between checks — treat as free
  }
  if (Date.now() - stat.mtimeMs > STALE_MS) return true;
  const pid = Number.parseInt(raw.split(/\s/)[0] ?? "", 10);
  if (!Number.isFinite(pid) || pid <= 0) return true;
  if (pid === process.pid) return false;
  try {
    process.kill(pid, 0); // signal 0 only probes for existence
    return false;
  } catch (err) {
    // EPERM means the process exists but belongs to someone else.
    return (err as NodeJS.ErrnoException).code !== "EPERM";
  }
}

export async function acquire(store: string, timeoutMs = DEFAULT_TIMEOUT_MS, env: Env = process.env): Promise<() => void> {
  const file = lockFileFor(store, env);
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      const fd = fs.openSync(file, "wx"); // atomic create-or-fail
      fs.writeFileSync(fd, `${process.pid} ${new Date().toISOString()}\n`);
      fs.closeSync(fd);
      let released = false;
      return () => {
        if (released) return;
        released = true;
        try {
          fs.unlinkSync(file);
        } catch {
          // already gone: someone reclaimed it as stale
        }
      };
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code !== "EEXIST") throw err;
      if (holderIsGone(file)) {
        try {
          fs.unlinkSync(file);
        } catch {
          // lost the race to another reclaimer; just retry
        }
        continue;
      }
      if (Date.now() >= deadline) throw new LockTimeoutError(file);
      await sleep(POLL_MS);
    }
  }
}

export async function withLock<T>(store: string, fn: () => Promise<T> | T, timeoutMs = DEFAULT_TIMEOUT_MS): Promise<T> {
  const release = await acquire(store, timeoutMs);
  try {
    return await fn();
  } finally {
    release();
  }
}
