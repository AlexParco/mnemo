import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";

/** Walk up until the repo root — the layout differs between `test/` and the
 * compiled `dist/test/`, so nothing here may assume a fixed depth. */
export function repoRoot(): string {
  let dir = import.meta.dirname;
  for (let i = 0; i < 10; i++) {
    if (fs.existsSync(path.join(dir, "templates", "SCHEMA.md"))) return dir;
    const up = path.dirname(dir);
    if (up === dir) break;
    dir = up;
  }
  throw new Error("repo root not found from " + import.meta.dirname);
}

export const ROOT = repoRoot();
export const CARD_PY = path.join(ROOT, "server", "test", "oracle", "card.py");
export const FIXTURE_STORE = path.join(ROOT, "server", "test", "fixtures", "store");

export const FIXTURE_PROJECTS = ["orion-api", "atlas-web", "quiet-shed", "odd-corners", "mixed-tongues"] as const;

/** Tests must never inherit the developer's real mnemo setup: an ambient
 * MNEMO_REMOTE would point a temp store at the user's actual memory hub. */
export function isolateEnv(): void {
  for (const key of ["MNEMO_DIR", "MNEMO_REMOTE", "MNEMO_MACHINE", "MNEMO_LANG"]) {
    delete process.env[key];
  }
}

export function hasPython(): boolean {
  return spawnSync("python3", ["-c", ""], { encoding: "utf8" }).status === 0;
}

export interface CardPyResult {
  stdout: string;
  stderr: string;
  status: number | null;
}

/** Run the original `card.py` against a store, with the environment pinned so
 * the comparison is about the render and nothing else. */
export function runCardPy(store: string, slug: string, lang: string, machine: string): CardPyResult {
  const env = { ...process.env };
  delete env.MNEMO_LANG;
  const res = spawnSync("python3", [CARD_PY, slug, lang], {
    encoding: "utf8",
    env: { ...env, MNEMO_DIR: store, MNEMO_MACHINE: machine },
  });
  if (res.error) throw res.error;
  return { stdout: res.stdout, stderr: res.stderr, status: res.status };
}

const created: string[] = [];

/** A throwaway directory, tracked so the suite does not litter /tmp across runs.
 * Pair it with `after(cleanupTempDirs)`. */
export function tempDir(prefix: string): string {
  const dir = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), prefix));
  created.push(dir);
  return dir;
}

export function cleanupTempDirs(): void {
  for (const dir of created.splice(0)) {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}
