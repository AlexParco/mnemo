/** The store's reading rules. The decoy cases are the point: these are the
 * failures the skills work around with prose, and that the port must make
 * structurally impossible. */

import assert from "node:assert/strict";
import test, { describe } from "node:test";
import { loadMemories, memoriesForProject, summaryFromBody, typeRank } from "../src/store/memory.js";
import { overview, searchMemories, loadProjectContext } from "../src/store/store.js";
import { readProject, projectSlugs } from "../src/store/project.js";
import { FIXTURE_STORE } from "./helpers.js";

describe("project filtering", () => {
  const memories = loadMemories(FIXTURE_STORE);

  test("a longer slug is not a match for a shorter one", () => {
    const ids = memoriesForProject(memories, "orion-api").map((m) => m.id);
    assert.ok(!ids.includes("decoy-orion-api-v2"), "orion-api-v2 must not count as orion-api");
    assert.deepEqual(memoriesForProject(memories, "orion-api-v2").map((m) => m.id), ["decoy-orion-api-v2"]);
  });

  test("a mention in the body is not a tag", () => {
    const decoy = memories.find((m) => m.id === "decoy-orion-api-v2")!;
    assert.ok(decoy.body.includes("orion-api"), "fixture must keep the prose mention");
    assert.deepEqual(decoy.projects, ["orion-api-v2"]);
  });

  test("a scalar projects value still matches", () => {
    assert.ok(memoriesForProject(memories, "atlas-web").some((m) => m.id === "atlas-scalar-project"));
  });

  test("dotfile memories are included, as pathlib's glob does", () => {
    assert.ok(memories.some((m) => m.id === ".editor-backup"));
  });

  test("overlap is counted once per project", () => {
    const shared = memories.find((m) => m.id === "shared-timezone-trap")!;
    assert.deepEqual(shared.projects, ["orion-api", "atlas-web"]);
    assert.ok(memoriesForProject(memories, "orion-api").includes(shared));
    assert.ok(memoriesForProject(memories, "atlas-web").includes(shared));
  });

  test("ordering is by type, then stable by file name", () => {
    const ordered = memoriesForProject(memories, "orion-api");
    const ranks = ordered.map((m) => typeRank(m.type));
    assert.deepEqual(ranks, [...ranks].sort((a, b) => a - b), "types must be grouped in SCHEMA order");
    const decisions = ordered.filter((m) => m.type === "decision").map((m) => m.id);
    assert.deepEqual(decisions, ["orion-aaa-first-decision", "orion-auth-jwt-rotation", "orion-md-markers"]);
  });
});

describe("summaries", () => {
  test("leading markdown markers are stripped, including bold", () => {
    assert.equal(summaryFromBody("\n- **Decision:** keep it\n"), "Decision:** keep it");
  });
  test("an empty body has no summary", () => {
    assert.equal(summaryFromBody("\n\n  \n"), "");
  });
});

describe("overview", () => {
  const o = overview(FIXTURE_STORE);

  test("active projects sort first", () => {
    assert.equal(o.projects[0]!.slug, "orion-api");
    assert.deepEqual(o.projects.map((p) => p.status), ["active", "active", "paused", "done", "archived"]);
  });

  test("counts split total from shared", () => {
    const orion = o.projects.find((p) => p.slug === "orion-api")!;
    assert.equal(orion.shared, 1, "only the timezone note is multi-tagged");
    assert.equal(orion.memories, 12);
  });

  test("a memory tagged to no existing project is reported as orphan", () => {
    assert.equal(o.totals.orphanMemories, 1);
  });

  test("a project with no memories and no pending file is still a project", () => {
    const quiet = o.projects.find((p) => p.slug === "quiet-shed")!;
    assert.equal(quiet.memories, 0);
    assert.equal(readProject(FIXTURE_STORE, "quiet-shed")!.hasPending, false);
  });
});

describe("search", () => {
  test("matches the body, case-insensitively", () => {
    const hits = searchMemories(FIXTURE_STORE, { query: "REFRESH TOKENS" });
    assert.deepEqual(hits.map((m) => m.id), ["orion-auth-jwt-rotation"]);
  });

  test("a spaced query matches a kebab-case id", () => {
    assert.deepEqual(searchMemories(FIXTURE_STORE, { query: "rate limit" }).map((m) => m.id), ["orion-rate-limit-invariant"]);
  });

  test("every term must appear, not just one", () => {
    assert.equal(searchMemories(FIXTURE_STORE, { query: "cursor unicorn" }).length, 0);
    assert.ok(searchMemories(FIXTURE_STORE, { query: "cursor" }).length > 0);
  });
  test("filters compose", () => {
    const hits = searchMemories(FIXTURE_STORE, { project: "orion-api", type: "constraint" });
    assert.deepEqual(hits.map((m) => m.id), ["orion-rate-limit-invariant"]);
  });
  test("limit is honoured", () => {
    assert.equal(searchMemories(FIXTURE_STORE, { project: "orion-api", limit: 3 }).length, 3);
  });
});

describe("loadProjectContext", () => {
  test("returns the card, the memories and the shared conventions", () => {
    const ctx = loadProjectContext(FIXTURE_STORE, "orion-api", "en")!;
    assert.ok(ctx.card.startsWith("📁 orion-api · active"));
    assert.equal(ctx.memories.length, 12);
    assert.ok(ctx.shared.some((s) => s.name === "CONVENTIONS.md"));
  });
  test("an unknown slug is null, not an exception", () => {
    assert.equal(loadProjectContext(FIXTURE_STORE, "nope", "en"), null);
  });
  test("directories are the source of truth for slugs", () => {
    assert.deepEqual(projectSlugs(FIXTURE_STORE), ["atlas-web", "mixed-tongues", "odd-corners", "orion-api", "quiet-shed"]);
  });
});
