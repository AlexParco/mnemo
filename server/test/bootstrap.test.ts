/** Provisioning, including the two-machine flow that is P2's acceptance
 * criterion: a second machine pointed at the same hub must adopt the memory that
 * is already there, not start an empty store beside it. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test, { after, describe } from "node:test";
import { ensureStore } from "../src/store/bootstrap.js";
import { commitStore, upsertProject, writeMemory } from "../src/store/write.js";
import { gitRun, gitTry } from "../src/git/exec.js";
import { gitStatus } from "../src/git/read.js";
import { readProject } from "../src/store/project.js";
import { loadMemories } from "../src/store/memory.js";
import { cleanupTempDirs, isolateEnv, ROOT, tempDir } from "./helpers.js";

isolateEnv();
after(cleanupTempDirs);

/** A store needs a committer identity; the test box's global config is not
 * something the suite may depend on. */
function withIdentity(store: string): string {
  gitRun(store, ["config", "user.name", "Fixture Person"]);
  gitRun(store, ["config", "user.email", "fixture@example.invalid"]);
  return store;
}

describe("ensureStore", () => {
  test("provisions a fresh store", () => {
    const store = path.join(tempDir("mnemo-boot-"), "store");
    const report = ensureStore(store, {});
    assert.equal(report.createdStore, true);
    assert.equal(report.initialisedRepo, true);
    for (const sub of ["projects", "memories", "shared"]) {
      assert.ok(fs.existsSync(path.join(store, sub)), `${sub}/ must exist`);
    }
    assert.equal(gitStatus(store).branch, "main", "a store with no commits still reports its branch");
    assert.equal(fs.readFileSync(path.join(store, ".gitignore"), "utf8"), ".DS_Store\n");
    assert.deepEqual(report.wroteTemplates, [".gitignore", "shared/SCHEMA.md"]);
  });

  test("the SCHEMA it writes is the repo's contract, not a copy that drifted", () => {
    const store = tempDir("mnemo-boot-");
    ensureStore(store, {});
    assert.equal(
      fs.readFileSync(path.join(store, "shared", "SCHEMA.md"), "utf8"),
      fs.readFileSync(path.join(ROOT, "templates", "SCHEMA.md"), "utf8"),
    );
  });

  test("is idempotent", () => {
    const store = tempDir("mnemo-boot-");
    ensureStore(store, {});
    fs.writeFileSync(path.join(store, ".gitignore"), "custom\n");
    const second = ensureStore(store, {});
    assert.equal(second.createdStore, false);
    assert.equal(second.initialisedRepo, false);
    assert.deepEqual(second.wroteTemplates, [], "existing files must be left alone");
    assert.equal(fs.readFileSync(path.join(store, ".gitignore"), "utf8"), "custom\n");
  });

  test("wires a remote without adopting when the hub is empty", () => {
    const hub = tempDir("mnemo-hub-");
    gitRun(hub, ["init", "--bare", "-q", "-b", "main"]);
    const store = tempDir("mnemo-boot-");
    const report = ensureStore(store, { MNEMO_REMOTE: hub });
    assert.equal(report.wiredRemote, hub);
    assert.equal(report.adoptedHistory, false);
    assert.equal(gitTry(store, ["remote", "get-url", "origin"]), hub);
  });

  test("a hub with its own memory never overwrites a store that already has content", async () => {
    // The README's documented path: create a store locally, wire the remote later.
    const hub = tempDir("mnemo-hub-");
    gitRun(hub, ["init", "--bare", "-q", "-b", "main"]);
    const seed = tempDir("mnemo-seed-");
    ensureStore(seed, { MNEMO_REMOTE: hub });
    withIdentity(seed);
    await commitStore(seed, "save: the hub has real files, not an empty tree");
    gitRun(seed, ["push", "-q", "-u", "origin", "main"]);

    const local = tempDir("mnemo-local-");
    ensureStore(local, {}); // local first, no remote: writes .gitignore and SCHEMA.md
    const report = ensureStore(local, { MNEMO_REMOTE: hub });

    assert.equal(report.adoptedHistory, false);
    assert.match(report.adoptionBlocked ?? "", /already has memory[\s\S]*Reconcile them by hand/);
    assert.equal(report.wiredRemote, hub, "the remote is still wired");
    assert.ok(fs.existsSync(path.join(local, "shared", "SCHEMA.md")), "the local store stays intact and usable");
  });

  test("an unreachable hub does not block a local store", () => {
    const store = tempDir("mnemo-boot-");
    const report = ensureStore(store, { MNEMO_REMOTE: path.join(tempDir("mnemo-gone-"), "nope.git") });
    assert.equal(report.adoptedHistory, false);
    assert.equal(report.initialisedRepo, true, "the store is still usable offline");
  });
});

describe("two machines, one hub", () => {
  test("the second machine adopts the memory already on the hub", async () => {
    const hub = tempDir("mnemo-hub-");
    gitRun(hub, ["init", "--bare", "-q", "-b", "main"]);

    // Machine A: provision, save something, commit, publish.
    const a = tempDir("mnemo-a-");
    ensureStore(a, { MNEMO_REMOTE: hub });
    withIdentity(a);
    await upsertProject(a, { slug: "orion-api", name: "Orion API", services: ["api"] });
    await writeMemory(a, {
      id: "orion-auth-rotation",
      projects: ["orion-api"],
      type: "decision",
      body: "Access tokens rotate every 15 minutes.",
    });
    await commitStore(a, "save(orion-api): token rotation");
    // Push is P3; the test does it by hand because adoption is what is under test.
    gitRun(a, ["push", "-q", "-u", "origin", "main"]);

    // Machine B: nothing local at all — the path must not exist yet, or
    // `createdStore` would be reporting on mkdtemp rather than on bootstrap.
    const b = path.join(tempDir("mnemo-b-"), "store");
    const report = ensureStore(b, { MNEMO_REMOTE: hub });

    assert.equal(report.adoptedHistory, true, "B must adopt, not start empty");
    assert.equal(report.createdStore, true);
    assert.deepEqual(report.wroteTemplates, [], "the templates came from the hub, so none were written");

    const project = readProject(b, "orion-api");
    assert.ok(project, "A's project must be present on B");
    assert.equal(project!.name, "Orion API");
    assert.deepEqual(loadMemories(b).map((m) => m.id), ["orion-auth-rotation"]);

    // If the templates had been written before the checkout, they would sit
    // untracked in its way. A clean worktree is the proof the order held.
    assert.equal(gitTry(b, ["status", "--porcelain"]), "");
  });
});
