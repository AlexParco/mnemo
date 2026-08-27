/** P4 acceptance: deleting a project must leave every shared memory alive and
 * correctly untagged — checked by reading the parsed `projects` field, which is
 * the whole point: a grep for the slug matches prose and longer slugs too. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test, { after, describe } from "node:test";
import { ensureStore } from "../src/store/bootstrap.js";
import { applyForget, applyRename, planForget, planRename } from "../src/store/destructive.js";
import { commitStore, upsertProject, writeMemory, writePending } from "../src/store/write.js";
import { gitRun, gitTry } from "../src/git/exec.js";
import { loadMemories } from "../src/store/memory.js";
import { readProject, projectSlugs } from "../src/store/project.js";
import { memoryPath, projectDir } from "../src/store/paths.js";
import { cleanupTempDirs, isolateEnv, tempDir } from "./helpers.js";

isolateEnv();
after(cleanupTempDirs);

const memoryOf = (store: string, id: string) => loadMemories(store).find((m) => m.id === id);

/** Two projects, and every shape the classification has to tell apart. */
async function seeded(): Promise<string> {
  const store = path.join(tempDir("mnemo-destr-"), "store");
  ensureStore(store, {});
  gitRun(store, ["config", "user.name", "Fixture Person"]);
  gitRun(store, ["config", "user.email", "fixture@example.invalid"]);
  await upsertProject(store, { slug: "orion-api", name: "Orion API" });
  await upsertProject(store, { slug: "orion-api-v2", name: "Orion API v2" });
  await upsertProject(store, { slug: "atlas-web", name: "Atlas Web" });

  await writeMemory(store, { id: "orion-only-a", projects: ["orion-api"], type: "decision", body: "Only orion." });
  await writeMemory(store, { id: "orion-only-b", projects: ["orion-api"], type: "bug", body: "Also only orion." });
  await writeMemory(store, { id: "shared-both", projects: ["orion-api", "atlas-web"], type: "gotcha", body: "Shared fact." });
  await writeMemory(store, { id: "atlas-only", projects: ["atlas-web"], type: "reference", body: "Atlas only." });
  await writeMemory(store, { id: "v2-only", projects: ["orion-api-v2"], type: "decision", body: "The longer slug." });
  await writeMemory(store, {
    id: "decoy-mention", projects: ["atlas-web"], type: "reference",
    body: "This body names orion-api on purpose, and even writes projects: [orion-api] as prose.",
  });
  await writeMemory(store, { id: "linker", projects: ["atlas-web"], type: "reference", body: "See [[orion-only-a]] for context." });

  // A key this version knows nothing about, to prove the untag is surgical.
  const file = memoryPath(store, "shared-both");
  fs.writeFileSync(file, fs.readFileSync(file, "utf8").replace("type: gotcha", "type: gotcha\nreviewed-by: a-future-field"));

  await commitStore(store, "save: seed");
  return store;
}

const rejects = (fn: () => Promise<unknown>, pattern: RegExp) =>
  assert.rejects(fn, (err: Error) => {
    assert.equal(err.name, "StoreError");
    assert.match(err.message, pattern);
    return true;
  });

describe("forget a project", () => {
  test("the plan splits exclusive from shared before anything happens", async () => {
    const store = await seeded();
    const plan = await planForget(store, "project", "orion-api");
    assert.equal(plan.kind, "forget-project");
    if (plan.kind !== "forget-project") return;
    assert.deepEqual(plan.exclusive.sort(), ["orion-only-a", "orion-only-b"]);
    assert.deepEqual(plan.shared, [{ id: "shared-both", remaining: ["atlas-web"] }]);
    assert.deepEqual(plan.brokenLinks, ["memories/linker.md"]);
    assert.equal(loadMemories(store).length, 7, "planning must not touch anything");
  });

  test("applying keeps every shared memory alive, tagged only with what remains", async () => {
    const store = await seeded();
    const plan = await planForget(store, "project", "orion-api");
    const result = await applyForget(store, "project", "orion-api", plan.token);

    const shared = memoryOf(store, "shared-both");
    assert.ok(shared, "the shared memory must survive");
    assert.deepEqual(shared!.projects, ["atlas-web"], "read from the parsed field, not a grep");
    assert.deepEqual(result.untagged, [{ id: "shared-both", remaining: ["atlas-web"] }]);

    assert.equal(memoryOf(store, "orion-only-a"), undefined);
    assert.equal(memoryOf(store, "orion-only-b"), undefined);
    assert.equal(fs.existsSync(projectDir(store, "orion-api")), false);
    assert.deepEqual(projectSlugs(store), ["atlas-web", "orion-api-v2"]);
  });

  test("untagging is surgical: unknown keys and the body survive", async () => {
    const store = await seeded();
    const before = fs.readFileSync(memoryPath(store, "shared-both"), "utf8");
    const plan = await planForget(store, "project", "orion-api");
    await applyForget(store, "project", "orion-api", plan.token);
    const after = fs.readFileSync(memoryPath(store, "shared-both"), "utf8");
    assert.match(after, /reviewed-by: a-future-field/);
    assert.match(after, /\n\nShared fact\.\n$/);
    assert.equal(after.split("\n").length, before.split("\n").length, "no lines added or lost");
  });

  test("a slug in prose is not a tag, and a longer slug is not a match", async () => {
    const store = await seeded();
    const plan = await planForget(store, "project", "orion-api");
    await applyForget(store, "project", "orion-api", plan.token);
    const decoy = memoryOf(store, "decoy-mention");
    assert.ok(decoy, "a memory that merely names the slug must survive");
    assert.match(decoy!.body, /projects: \[orion-api\] as prose/, "and its prose must be untouched");
    assert.ok(memoryOf(store, "v2-only"), "orion-api-v2 is a different project");
    assert.ok(readProject(store, "orion-api-v2"), "and its directory stays");
  });

  test("dangling links are reported, never silently repaired", async () => {
    const store = await seeded();
    const plan = await planForget(store, "project", "orion-api");
    const result = await applyForget(store, "project", "orion-api", plan.token);
    assert.deepEqual(result.brokenLinks, ["memories/linker.md"]);
    assert.match(memoryOf(store, "linker")!.body, /\[\[orion-only-a\]\]/);
  });

  test("it makes its own commit, named for what it did", async () => {
    const store = await seeded();
    const plan = await planForget(store, "project", "orion-api");
    const result = await applyForget(store, "project", "orion-api", plan.token);
    assert.equal(result.commit.committed, true);
    assert.equal(gitTry(store, ["log", "-1", "--pretty=%s"]), "forget(project orion-api): 2 deleted, 1 untagged");
  });

  test("an unknown project is refused with the real slugs", async () => {
    const store = await seeded();
    await rejects(() => planForget(store, "project", "ghost"), /No project 'ghost'.*Existing: atlas-web, orion-api, orion-api-v2/s);
  });
});

describe("the confirmation is bound to the plan", () => {
  test("a memory tagged with the slug after planning invalidates it", async () => {
    const store = await seeded();
    const stale = (await planForget(store, "project", "orion-api")).token;
    await writeMemory(store, { id: "orion-late", projects: ["orion-api"], type: "todo", body: "Arrived after the plan." });
    await commitStore(store, "save: late arrival");
    await rejects(() => applyForget(store, "project", "orion-api", stale), /does not match the current state/);
    assert.ok(memoryOf(store, "orion-late"), "nothing was deleted");
  });

  test("a wrong value deletes nothing", async () => {
    const store = await seeded();
    await rejects(() => applyForget(store, "project", "orion-api", "000000000000"), /Nothing was deleted/);
    assert.ok(readProject(store, "orion-api"));
  });

  test("applying twice cannot happen: the second plan is a different plan", async () => {
    const store = await seeded();
    const plan = await planForget(store, "project", "orion-api");
    await applyForget(store, "project", "orion-api", plan.token);
    await rejects(() => applyForget(store, "project", "orion-api", plan.token), /No project 'orion-api'/);
  });

  test("uncommitted work is not folded into the deletion commit", async () => {
    const store = await seeded();
    const plan = await planForget(store, "project", "orion-api");
    await writePending(store, "atlas-web", "# Pending — Atlas Web\n\n## Next\n- [ ] unrelated work\n");
    await rejects(() => applyForget(store, "project", "orion-api", plan.token), /uncommitted changes.*mnemo_commit/s);
    assert.ok(readProject(store, "orion-api"), "and nothing was deleted");
  });
});

describe("forget a memory", () => {
  test("plans, deletes, and flags what pointed at it", async () => {
    const store = await seeded();
    const plan = await planForget(store, "memory", "orion-only-a");
    assert.equal(plan.kind, "forget-memory");
    if (plan.kind !== "forget-memory") return;
    assert.deepEqual(plan.projects, ["orion-api"]);
    assert.equal(plan.summary, "Only orion.");
    assert.deepEqual(plan.brokenLinks, ["memories/linker.md"]);

    const result = await applyForget(store, "memory", "orion-only-a", plan.token);
    assert.equal(memoryOf(store, "orion-only-a"), undefined);
    assert.deepEqual(result.brokenLinks, ["memories/linker.md"]);
    assert.equal(gitTry(store, ["log", "-1", "--pretty=%s"]), "forget(memory orion-only-a)");
  });

  test("an unknown id is refused", async () => {
    const store = await seeded();
    await rejects(() => planForget(store, "memory", "never-existed"), /No memory 'never-existed'/);
  });
});

describe("rename", () => {
  test("the plan names every memory that would be rewritten", async () => {
    const store = await seeded();
    const plan = await planRename(store, "orion-api", "orion-core");
    assert.deepEqual(plan.memories.sort(), ["orion-only-a", "orion-only-b", "shared-both"]);
  });

  test("applying moves the project and retags, overlap and order intact", async () => {
    const store = await seeded();
    const plan = await planRename(store, "orion-api", "orion-core");
    const result = await applyRename(store, "orion-api", "orion-core", plan.token);

    assert.equal(readProject(store, "orion-api"), null);
    assert.equal(readProject(store, "orion-core")!.name, "Orion API", "the readable name is untouched");
    assert.equal(readProject(store, "orion-core")!.fm["slug"], "orion-core");
    assert.deepEqual(memoryOf(store, "shared-both")!.projects, ["orion-core", "atlas-web"], "order preserved");
    assert.deepEqual(memoryOf(store, "orion-only-a")!.projects, ["orion-core"]);
    assert.equal(result.memoriesUpdated.length, 3);
    assert.equal(gitTry(store, ["log", "-1", "--pretty=%s"]), "rename(orion-api → orion-core): 3 memories retagged");
  });

  test("prose that names the old slug is left alone", async () => {
    const store = await seeded();
    const plan = await planRename(store, "orion-api", "orion-core");
    await applyRename(store, "orion-api", "orion-core", plan.token);
    const decoy = memoryOf(store, "decoy-mention")!;
    assert.match(decoy.body, /names orion-api on purpose/);
    assert.deepEqual(decoy.projects, ["atlas-web"]);
  });

  test("a longer slug that contains the old one is not renamed with it", async () => {
    const store = await seeded();
    const plan = await planRename(store, "orion-api", "orion-core");
    await applyRename(store, "orion-api", "orion-core", plan.token);
    assert.ok(readProject(store, "orion-api-v2"), "orion-api-v2 is its own project");
    assert.deepEqual(memoryOf(store, "v2-only")!.projects, ["orion-api-v2"]);
  });

  test("refuses a merge, an invalid slug, and a no-op", async () => {
    const store = await seeded();
    await rejects(() => planRename(store, "orion-api", "atlas-web"), /already exists.*would merge two projects/s);
    await rejects(() => planRename(store, "orion-api", "Not Kebab"), /not a valid slug/);
    await rejects(() => planRename(store, "orion-api", "orion-api"), /are the same/);
    await rejects(() => planRename(store, "ghost", "whatever"), /No project 'ghost'/);
  });

  test("the confirmation goes stale like the forget one does", async () => {
    const store = await seeded();
    const stale = (await planRename(store, "orion-api", "orion-core")).token;
    await writeMemory(store, { id: "orion-late", projects: ["orion-api"], type: "todo", body: "Late." });
    await commitStore(store, "save: late");
    await rejects(() => applyRename(store, "orion-api", "orion-core", stale), /does not match the current state/);
    assert.ok(readProject(store, "orion-api"), "nothing was renamed");
  });
});
