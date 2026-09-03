/** P6 acceptance: the plugin and the MCP server run against one store on one
 * machine. Both go through the same lock, but the lock is a file — so the thing
 * that has to hold is contention between PROCESSES, which promises in a single
 * process cannot demonstrate. */

import assert from "node:assert/strict";
import path from "node:path";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import test, { after, describe } from "node:test";
import { ensureStore } from "../src/store/bootstrap.js";
import { upsertProject, commitStore } from "../src/store/write.js";
import { gitRun, gitTry } from "../src/git/exec.js";
import { loadMemories } from "../src/store/memory.js";
import { lockFileFor } from "../src/store/lock.js";
import { cleanupTempDirs, isolateEnv, tempDir } from "./helpers.js";
import fs from "node:fs";

isolateEnv();
after(cleanupTempDirs);

const WORKER = path.join(path.dirname(fileURLToPath(import.meta.url)), "worker", "write-one.js");

describe("many writers, one store", () => {
  test("every concurrent write survives and the tree is left clean", async () => {
    const store = path.join(tempDir("mnemo-contend-"), "store");
    ensureStore(store, {});
    gitRun(store, ["config", "user.name", "Fixture Person"]);
    gitRun(store, ["config", "user.email", "fixture@example.invalid"]);
    await upsertProject(store, { slug: "orion-api", name: "Orion API" });
    await commitStore(store, "save: seed");

    const WORKERS = 6;
    const CYCLES = 4;
    const ids = Array.from({ length: WORKERS }, (_, w) =>
      Array.from({ length: CYCLES }, (_, i) => `w${w}-${i}`),
    ).flat();

    // Each worker loops, so they overlap on the store instead of staggering on
    // node's startup. Spawned, not execFileSync: a synchronous spawn inside a
    // Promise executor runs to completion before the next one starts, so the
    // workers would be serial and the test would prove nothing.
    await Promise.all(
      Array.from({ length: WORKERS }, (_, w) =>
        new Promise<void>((resolve, reject) => {
          const child = spawn(process.execPath, [WORKER, store, `w${w}`, String(CYCLES)], {
            stdio: ["ignore", "ignore", "pipe"],
          });
          let stderr = "";
          child.stderr.on("data", (chunk: Buffer) => (stderr += chunk.toString()));
          child.on("error", reject);
          child.on("close", (code) =>
            code === 0 ? resolve() : reject(new Error(`worker w${w} exited ${code}: ${stderr.trim()}`)),
          );
        }),
      ),
    );

    // Nothing lost: a lock that let two writers in would drop one of these.
    assert.deepEqual(loadMemories(store).map((m) => m.id).sort(), [...ids].sort());

    // Nothing left uncommitted: each writer commits after its own write, so the
    // last operation is always a commit, even when an earlier one swept its file.
    assert.equal(gitTry(store, ["status", "--porcelain"]), "");

    const tracked = (gitTry(store, ["ls-tree", "-r", "HEAD", "--name-only"]) ?? "").split("\n");
    for (const id of ids) assert.ok(tracked.includes(`memories/${id}.md`), `${id} is not in the commit`);

    // Commits may be fewer than writers: `add -A` means one commit can carry
    // another writer's file. That is a split batch, not a loss — the documented
    // trade-off — so what matters is the count is sane, not that it is six.
    const commits = Number.parseInt(gitTry(store, ["rev-list", "--count", "HEAD"]) ?? "0", 10);
    assert.ok(commits >= 2 && commits <= ids.length + 2, `unexpected commit count: ${commits}`);

    assert.equal(gitTry(store, ["fsck", "--no-progress"]), "", "the object database must be intact");
    assert.equal(fs.existsSync(lockFileFor(store)), false, "no writer may leave the lock behind");
  });
});
