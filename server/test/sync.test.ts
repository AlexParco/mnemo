/** P3 acceptance: the whole sync loop against a local bare repo acting as the
 * hub — two machines, a real conflict, and a push that refuses to leak. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test, { after, describe } from "node:test";
import { ensureStore } from "../src/store/bootstrap.js";
import { commitStore, upsertProject, writeMemory, writePending } from "../src/store/write.js";
import { pushStore, rebaseAction, resolveConflict, syncStore } from "../src/git/sync.js";
import { gitRun, gitTry } from "../src/git/exec.js";
import { loadMemories } from "../src/store/memory.js";
import { readProject } from "../src/store/project.js";
import { pendingPath } from "../src/store/paths.js";
import { cleanupTempDirs, isolateEnv, tempDir } from "./helpers.js";

isolateEnv();
after(cleanupTempDirs);

function identity(store: string, who: string): string {
  gitRun(store, ["config", "user.name", who]);
  gitRun(store, ["config", "user.email", `${who}@example.invalid`]);
  return store;
}

/** A hub plus a machine that has already published a project. */
async function hubWithOrigin(): Promise<{ hub: string; a: string }> {
  const hub = tempDir("mnemo-hub-");
  gitRun(hub, ["init", "--bare", "-q", "-b", "main"]);
  const a = path.join(tempDir("mnemo-a-"), "store");
  ensureStore(a, { MNEMO_REMOTE: hub });
  identity(a, "machine-a");
  await upsertProject(a, { slug: "orion-api", name: "Orion API" });
  await writePending(a, "orion-api", "# Pending — Orion API\n\n## In progress\n- [ ] shared task\n");
  await commitStore(a, "save(orion-api): project");
  await pushStore(a);
  return { hub, a };
}

async function joiner(hub: string, who: string): Promise<string> {
  const store = path.join(tempDir(`mnemo-${who}-`), "store");
  ensureStore(store, { MNEMO_REMOTE: hub });
  return identity(store, who);
}

describe("push", () => {
  test("publishes, and says there is nothing left to publish", async () => {
    const { a } = await hubWithOrigin();
    const again = await pushStore(a);
    assert.equal(again.pushed, false);
    assert.equal(again.refused, "nothing-to-push");
  });

  test("a store with no remote is told so, not failed", async () => {
    const store = tempDir("mnemo-local-");
    ensureStore(store, {});
    const result = await pushStore(store);
    assert.equal(result.refused, "no-remote");
  });

  test("a second machine receives what the first published", async () => {
    const { hub } = await hubWithOrigin();
    const b = await joiner(hub, "machine-b");
    assert.equal(readProject(b, "orion-api")!.name, "Orion API");
  });
});

describe("push blocks secrets", () => {
  // Assembled at runtime so the literal never sits in a committed file.
  const LEAK = "AKIA" + "IOSFODNN7EXAMPLE";

  async function storeWithLeak(): Promise<string> {
    const { a } = await hubWithOrigin();
    await writeMemory(a, {
      id: "orion-deploy-key", projects: ["orion-api"], type: "reference",
      body: `The deploy user's key is ${LEAK} — pasted here by mistake.`,
    });
    await commitStore(a, "save(orion-api): deploy notes");
    return a;
  }

  test("refuses, reports where, and leaves the commit unpushed", async () => {
    const a = await storeWithLeak();
    const result = await pushStore(a);
    assert.equal(result.pushed, false);
    assert.equal(result.refused, "secrets");
    assert.equal(result.findings.length, 1);
    assert.equal(result.findings[0]!.rule, "AWS access key id");
    assert.equal(result.findings[0]!.file, "memories/orion-deploy-key.md");
    assert.ok(!JSON.stringify(result.findings).includes(LEAK), "the secret must not travel in the finding");
    assert.ok((gitTry(a, ["rev-list", "--count", "origin/main..HEAD"]) ?? "") !== "0", "nothing may have left the machine");
  });

  test("the acknowledgement is what unblocks it, and only the right one", async () => {
    const a = await storeWithLeak();
    const refused = await pushStore(a);
    assert.equal((await pushStore(a, "not-the-token")).refused, "bad-acknowledgement");
    const allowed = await pushStore(a, refused.acknowledgeToken!);
    assert.equal(allowed.pushed, true);
  });

  test("an acknowledgement stops working once the commits change", async () => {
    const a = await storeWithLeak();
    const stale = (await pushStore(a)).acknowledgeToken!;
    await writeMemory(a, { id: "orion-extra", projects: ["orion-api"], type: "todo", body: "Something else." });
    await commitStore(a, "save(orion-api): more");
    const result = await pushStore(a, stale);
    assert.equal(result.refused, "bad-acknowledgement");
    assert.notEqual(result.acknowledgeToken, stale);
  });

  test("the scan runs on a first push too, when there is no upstream yet", async () => {
    const hub = tempDir("mnemo-hub-");
    gitRun(hub, ["init", "--bare", "-q", "-b", "main"]);
    const store = path.join(tempDir("mnemo-first-"), "store");
    ensureStore(store, { MNEMO_REMOTE: hub });
    identity(store, "machine-first");
    await upsertProject(store, { slug: "orion-api", name: "Orion API" });
    await writeMemory(store, {
      id: "orion-key", projects: ["orion-api"], type: "reference", body: `Key ${LEAK} pasted by mistake.`,
    });
    await commitStore(store, "save: first ever");
    assert.equal((await pushStore(store)).refused, "secrets");
  });
});

describe("sync", () => {
  test("brings in what another machine published", async () => {
    const { hub, a } = await hubWithOrigin();
    const b = await joiner(hub, "machine-b");
    await writeMemory(a, { id: "orion-from-a", projects: ["orion-api"], type: "decision", body: "Decided on A." });
    await commitStore(a, "save(orion-api): from a");
    await pushStore(a);

    const result = await syncStore(b);
    assert.equal(result.pulled, true);
    assert.deepEqual(result.conflicts, []);
    assert.deepEqual(loadMemories(b).map((m) => m.id), ["orion-from-a"]);
  });

  test("a store with no remote is a no-op, not a failure", async () => {
    const store = tempDir("mnemo-local-");
    ensureStore(store, {});
    const result = await syncStore(store);
    assert.equal(result.hasRemote, false);
    assert.equal(result.pulled, false);
  });
});

describe("a real conflict on pending.md", () => {
  /** Both machines rewrite the same lines; B publishes first. */
  async function diverge(): Promise<{ a: string; b: string }> {
    const { hub, a } = await hubWithOrigin();
    const b = await joiner(hub, "machine-b");

    await writePending(b, "orion-api", "# Pending — Orion API\n\n## In progress\n- [ ] task from B\n");
    await commitStore(b, "save(orion-api): b's plan");
    await pushStore(b);

    await writePending(a, "orion-api", "# Pending — Orion API\n\n## In progress\n- [ ] task from A\n");
    await commitStore(a, "save(orion-api): a's plan");
    return { a, b };
  }

  test("sync reports it instead of guessing", async () => {
    const { a } = await diverge();
    const result = await syncStore(a);
    assert.equal(result.pulled, false);
    assert.equal(result.rebaseInProgress, true);
    assert.deepEqual(result.conflicts.map((c) => c.file), ["projects/orion-api/pending.md"]);
    assert.match(result.conflicts[0]!.content, /^<<<<<<</m, "the agent gets the markers to merge from");
  });

  test("resolving with the union, then continuing, keeps both sides", async () => {
    const { a } = await diverge();
    await syncStore(a);
    const merged = "# Pending — Orion API\n\n## In progress\n- [ ] task from B\n- [ ] task from A\n";
    const { remaining } = await resolveConflict(a, "projects/orion-api/pending.md", merged);
    assert.deepEqual(remaining, []);

    const done = await rebaseAction(a, "continue");
    assert.equal(done.ok, true);
    assert.equal(done.rebaseInProgress, false);
    assert.equal(fs.readFileSync(pendingPath(a, "orion-api"), "utf8"), merged);

    const pushed = await pushStore(a);
    assert.equal(pushed.pushed, true);
  });

  test("content that still has markers is refused", async () => {
    const { a } = await diverge();
    const conflict = (await syncStore(a)).conflicts[0]!;
    await assert.rejects(
      () => resolveConflict(a, conflict.file, conflict.content),
      (err: Error) => {
        assert.equal(err.name, "StoreError");
        assert.match(err.message, /still contains conflict markers/);
        return true;
      },
    );
  });

  test("continue is refused while anything is unresolved", async () => {
    const { a } = await diverge();
    await syncStore(a);
    const result = await rebaseAction(a, "continue");
    assert.equal(result.ok, false);
    assert.deepEqual(result.remaining, ["projects/orion-api/pending.md"]);
  });

  test("abort puts the store back as it was", async () => {
    const { a } = await diverge();
    await syncStore(a);
    const aborted = await rebaseAction(a, "abort");
    assert.equal(aborted.ok, true);
    assert.equal(aborted.rebaseInProgress, false);
    assert.match(fs.readFileSync(pendingPath(a, "orion-api"), "utf8"), /task from A/);
    assert.equal(gitTry(a, ["log", "-1", "--pretty=%s"]), "save(orion-api): a's plan");
  });

  test("pushing mid-rebase is refused", async () => {
    const { a } = await diverge();
    await syncStore(a);
    assert.equal((await pushStore(a)).refused, "rebase-in-progress");
  });
});
