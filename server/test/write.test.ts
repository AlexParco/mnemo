/** Writes. What is asserted here is the mechanical half of the SCHEMA contract —
 * the half a model can forget and code cannot. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test, { after, describe } from "node:test";
import { ensureStore } from "../src/store/bootstrap.js";
import { commitStore, today, upsertProject, writeMemory, writePending } from "../src/store/write.js";
import { gitRun, gitTry } from "../src/git/exec.js";
import { parseDocument, renderDocument } from "../src/store/frontmatter.js";
import { loadMemories } from "../src/store/memory.js";
import { readProject } from "../src/store/project.js";
import { memoryPath } from "../src/store/paths.js";
import { cleanupTempDirs, isolateEnv, tempDir } from "./helpers.js";

isolateEnv();
after(cleanupTempDirs);

/** A provisioned store with a project and a committer identity. */
async function freshStore(): Promise<string> {
  const store = path.join(tempDir("mnemo-write-"), "store");
  ensureStore(store, {});
  gitRun(store, ["config", "user.name", "Fixture Person"]);
  gitRun(store, ["config", "user.email", "fixture@example.invalid"]);
  await upsertProject(store, { slug: "orion-api", name: "Orion API" });
  return store;
}

const rejects = (fn: () => Promise<unknown>, pattern: RegExp) =>
  assert.rejects(fn, (err: Error) => {
    assert.equal(err.name, "StoreError");
    assert.match(err.message, pattern);
    return true;
  });

describe("writeMemory", () => {
  test("writes exactly the SCHEMA shape", async () => {
    const store = await freshStore();
    const result = await writeMemory(store, {
      id: "orion-auth-rotation",
      projects: ["orion-api"],
      type: "decision",
      body: "Access tokens rotate every 15 minutes.",
    });
    assert.equal(result.created, true);
    assert.equal(
      fs.readFileSync(result.path, "utf8"),
      ["---", "id: orion-auth-rotation", "projects: [orion-api]", "type: decision",
       "author: Fixture Person", `updated: ${today()}`, "---", "", "Access tokens rotate every 15 minutes.", ""].join("\n"),
    );
  });

  test("optional fields appear only when given", async () => {
    const store = await freshStore();
    const { path: file } = await writeMemory(store, {
      id: "orion-cursor-trap", projects: ["orion-api"], type: "gotcha",
      services: ["api", "workers"], tags: ["pagination"], body: "Empty pages return a null cursor.",
    });
    const raw = fs.readFileSync(file, "utf8");
    assert.match(raw, /services: \[api, workers\]\ntags: \[pagination\]/);
    assert.equal(renderDocument(parseDocument(raw)), raw, "what it writes must round-trip");
  });

  test("refuses what the SCHEMA forbids", async () => {
    const store = await freshStore();
    const base = { projects: ["orion-api"], type: "decision" as const, body: "x" };
    await rejects(() => writeMemory(store, { ...base, id: "Not Kebab" }), /kebab-case/);
    await rejects(() => writeMemory(store, { ...base, id: "ok-id", type: "musing" as never }), /not a memory type/);
    await rejects(() => writeMemory(store, { ...base, id: "ok-id", body: "   " }), /needs a body/);
    await rejects(() => writeMemory(store, { ...base, id: "ok-id", projects: [] }), /at least one project/);
  });

  test("a project has to exist before a memory can be tagged with it", async () => {
    const store = await freshStore();
    await rejects(
      () => writeMemory(store, { id: "x-note", projects: ["ghost-project"], type: "decision", body: "x" }),
      /No project\(s\) 'ghost-project'.*Existing: orion-api.*never silently/s,
    );
  });

  test("an existing id is not clobbered without saying so", async () => {
    const store = await freshStore();
    const input = { id: "orion-note", projects: ["orion-api"], type: "decision" as const, body: "First." };
    await writeMemory(store, input);
    await rejects(() => writeMemory(store, { ...input, body: "Second." }), /already exists.*overwrite: true/s);
    assert.match(fs.readFileSync(memoryPath(store, "orion-note"), "utf8"), /First\./);
  });

  test("overwriting keeps unknown frontmatter and the original author", async () => {
    const store = await freshStore();
    await writeMemory(store, { id: "orion-note", projects: ["orion-api"], type: "decision", body: "First." });
    // A field this version knows nothing about, and an author from another machine.
    const file = memoryPath(store, "orion-note");
    fs.writeFileSync(file, fs.readFileSync(file, "utf8")
      .replace("author: Fixture Person", "author: Someone Else\nreviewed-by: a-future-field"));

    await writeMemory(store, { id: "orion-note", projects: ["orion-api"], type: "constraint", body: "Second.", overwrite: true });
    const raw = fs.readFileSync(file, "utf8");
    assert.match(raw, /reviewed-by: a-future-field/, "unknown keys must survive an update");
    assert.match(raw, /author: Someone Else/, "authorship is not reassigned by an edit");
    assert.match(raw, /type: constraint/);
    assert.match(raw, /\n\nSecond\.\n$/);
    assert.ok(!raw.includes("First."));
  });

  test("near-duplicates are surfaced, not blocked", async () => {
    const store = await freshStore();
    await writeMemory(store, { id: "orion-token-rotation", projects: ["orion-api"], type: "decision", body: "Rotate tokens." });
    const second = await writeMemory(store, { id: "orion-token-refresh", projects: ["orion-api"], type: "decision", body: "Refresh flow." });
    assert.deepEqual(second.related, ["orion-token-rotation"]);
  });
});

describe("writePending", () => {
  test("replaces the file and reports what it parsed", async () => {
    const store = await freshStore();
    const result = await writePending(store, "orion-api",
      "# Pending — Orion API\n\n## In progress\n- [ ] a\n- [x] b\n\n## Debt\n- [ ] c\n");
    assert.deepEqual(result.sections, [
      { label: "In progress", open: 1, done: 1 },
      { label: "Debt", open: 1, done: 0 },
    ]);
    assert.equal(readProject(store, "orion-api")!.pending.length, 2);
  });

  test("refuses an unknown project", async () => {
    const store = await freshStore();
    await rejects(() => writePending(store, "ghost", "# x\n"), /No project\(s\) 'ghost'/);
  });
});

describe("upsertProject", () => {
  test("creates INDEX.md and a pending file", async () => {
    const store = await freshStore();
    const result = await upsertProject(store, {
      slug: "atlas-web", name: "Atlas Web", status: "paused", services: ["web"], description: "The front end.",
    });
    assert.equal(result.created, true);
    const project = readProject(store, "atlas-web")!;
    assert.equal(project.name, "Atlas Web");
    assert.equal(project.status, "paused");
    assert.deepEqual(project.services, ["web"]);
    assert.equal(project.hasPending, true);
  });

  test("updating touches only the fields given", async () => {
    const store = await freshStore();
    await upsertProject(store, { slug: "orion-api", status: "paused" });
    const project = readProject(store, "orion-api")!;
    assert.equal(project.status, "paused");
    assert.equal(project.name, "Orion API", "the name must survive a status change");
    assert.equal(project.updated, today());
  });

  test("a new project needs a readable name, and a valid slug", async () => {
    const store = await freshStore();
    await rejects(() => upsertProject(store, { slug: "nameless" }), /needs a readable name/);
    await rejects(() => upsertProject(store, { slug: "Not Kebab", name: "x" }), /kebab-case/);
  });
});

describe("commitStore", () => {
  test("commits the batch and reports it is not on the hub", async () => {
    const store = await freshStore();
    await writeMemory(store, { id: "orion-note", projects: ["orion-api"], type: "decision", body: "A fact." });
    const result = await commitStore(store, "save(orion-api): a fact");
    assert.equal(result.committed, true);
    assert.ok(result.files.includes("memories/orion-note.md"));
    assert.equal(result.hasRemote, false);
    assert.equal(gitTry(store, ["log", "-1", "--pretty=%s"]), "save(orion-api): a fact");
  });

  test("strips Co-Authored-By: the store never carries it", async () => {
    const store = await freshStore();
    await writeMemory(store, { id: "orion-note", projects: ["orion-api"], type: "decision", body: "A fact." });
    const result = await commitStore(store, "save(orion-api): a fact\n\nCo-Authored-By: Someone <a@b.c>\n");
    assert.equal(result.strippedTrailers, 1);
    assert.ok(!(gitTry(store, ["log", "-1", "--pretty=%B"]) ?? "").includes("Co-Authored-By"));
  });

  test("a clean store is not an error", async () => {
    const store = await freshStore();
    await commitStore(store, "save: first");
    const second = await commitStore(store, "save: nothing new");
    assert.equal(second.committed, false);
  });

  test("without a git identity it refuses and says how to fix it", async () => {
    const saved = { g: process.env.GIT_CONFIG_GLOBAL, s: process.env.GIT_CONFIG_SYSTEM };
    const store = path.join(tempDir("mnemo-noid-"), "store");
    try {
      process.env.GIT_CONFIG_GLOBAL = "/dev/null";
      process.env.GIT_CONFIG_SYSTEM = "/dev/null";
      ensureStore(store, {});
      await rejects(() => commitStore(store, "save: x"), /no committer identity[\s\S]*git -C .* config user\.email/);
    } finally {
      process.env.GIT_CONFIG_GLOBAL = saved.g;
      process.env.GIT_CONFIG_SYSTEM = saved.s;
    }
  });
});

describe("concurrency", () => {
  test("parallel writes to one store all land", async () => {
    const store = await freshStore();
    await Promise.all(
      Array.from({ length: 8 }, (_, i) =>
        writeMemory(store, { id: `orion-note-${i}`, projects: ["orion-api"], type: "todo", body: `Fact ${i}.` }),
      ),
    );
    assert.equal(loadMemories(store).length, 8);
  });
});
