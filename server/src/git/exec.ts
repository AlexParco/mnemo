/** One place where git gets executed.
 *
 * `gitTry` answers questions ("where does this store stand?") and returns null
 * when git cannot answer. `gitRun` performs actions and throws with git's own
 * stderr attached, because a failed write must never look like a successful one. */

import { execFileSync } from "node:child_process";

export class GitError extends Error {
  constructor(readonly args: string[], readonly stderr: string) {
    super(`git ${args.join(" ")} failed: ${stderr.trim() || "no stderr"}`);
    this.name = "GitError";
  }
}

export function gitTry(store: string, args: string[]): string | null {
  try {
    return execFileSync("git", ["-C", store, ...args], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
  } catch {
    return null;
  }
}

export function gitRun(store: string, args: string[]): string {
  try {
    return execFileSync("git", ["-C", store, ...args], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      // Never let git open an editor or a credential prompt: this runs headless.
      env: { ...process.env, GIT_EDITOR: "true", GIT_TERMINAL_PROMPT: "0" },
    }).trim();
  } catch (err) {
    const e = err as { stderr?: Buffer | string };
    throw new GitError(args, typeof e.stderr === "string" ? e.stderr : (e.stderr?.toString() ?? ""));
  }
}

/** Resolved committer identity, or null when git has none configured. */
export function gitIdentity(store: string): { name: string; email: string } | null {
  const name = gitTry(store, ["config", "user.name"]);
  const email = gitTry(store, ["config", "user.email"]);
  return name && email ? { name, email } : null;
}
