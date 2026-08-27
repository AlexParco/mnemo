/** The lock only exists because MCP makes concurrency possible: the skills drove
 * git one model-step at a time, a server can be called by two agents at once. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test, { after, describe } from "node:test";
import { acquire, lockFileFor, withLock, LockTimeoutError } from "../src/store/lock.js";
import { cleanupTempDirs, tempDir } from "./helpers.js";

after(cleanupTempDirs);

const tmpStore = () => tempDir("mnemo-lock-");

describe("store lock", () => {
  test("a second holder waits and then times out", async () => {
    const store = tmpStore();
    const release = await acquire(store);
    await assert.rejects(() => acquire(store, 200), LockTimeoutError);
    release();
    const second = await acquire(store, 200);
    second();
  });

  test("releasing lets the next waiter in", async () => {
    const store = tmpStore();
    const order: string[] = [];
    const first = withLock(store, async () => {
      order.push("first-in");
      await new Promise((r) => setTimeout(r, 100));
      order.push("first-out");
    });
    const second = withLock(store, async () => {
      order.push("second-in");
    });
    await Promise.all([first, second]);
    assert.deepEqual(order, ["first-in", "first-out", "second-in"]);
  });

  test("a lock held by a dead process is reclaimed at once", async () => {
    const store = tmpStore();
    const file = lockFileFor(store);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    // A pid that cannot be running: the kernel caps pids well below this.
    fs.writeFileSync(file, "4294967295 2026-01-01T00:00:00.000Z\n");
    const release = await acquire(store, 500);
    release();
    assert.equal(fs.existsSync(file), false);
  });

  test("two stores do not block each other", async () => {
    const a = await acquire(tmpStore(), 200);
    const b = await acquire(tmpStore(), 200);
    a();
    b();
  });

  test("the lock file lives outside the store", async () => {
    const store = tmpStore();
    const release = await acquire(store);
    assert.deepEqual(fs.readdirSync(store), [], "nothing may appear in the store, or git would see it");
    release();
  });
});
