/** Store location and machine identity. Same environment contract as the
 * skills and `card.py`: MNEMO_DIR, XDG_DATA_HOME, MNEMO_MACHINE. */

import os from "node:os";
import path from "node:path";

export type Env = Record<string, string | undefined>;

/** `$MNEMO_DIR`, else `$XDG_DATA_HOME/mnemo`, else `~/.local/share/mnemo`.
 * An empty value counts as unset (card.py treats an empty XDG_DATA_HOME as a
 * literal, which would resolve to a relative path — that is not worth mirroring). */
export function storeDir(env: Env = process.env): string {
  if (env.MNEMO_DIR) return env.MNEMO_DIR;
  const base = env.XDG_DATA_HOME || path.join(os.homedir(), ".local", "share");
  return path.join(base, "mnemo");
}

/** This machine's label: `$MNEMO_MACHINE`, else the short hostname. */
export function machineLabel(env: Env = process.env): string {
  const explicit = env.MNEMO_MACHINE;
  if (explicit && explicit.trim()) return explicit.trim();
  return (os.hostname().split(".")[0] ?? "").trim();
}

export const projectsDir = (store: string) => path.join(store, "projects");
export const projectDir = (store: string, slug: string) => path.join(store, "projects", slug);
export const indexPath = (store: string, slug: string) => path.join(store, "projects", slug, "INDEX.md");
export const pendingPath = (store: string, slug: string) => path.join(store, "projects", slug, "pending.md");
export const memoriesDir = (store: string) => path.join(store, "memories");
export const memoryPath = (store: string, id: string) => path.join(store, "memories", `${id}.md`);
export const sharedDir = (store: string) => path.join(store, "shared");
