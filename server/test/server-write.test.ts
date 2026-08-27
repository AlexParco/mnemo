/** The write surface over the real transport, against a throwaway store. Its own
 * file so it gets its own process and cannot leak MNEMO_DIR into the read tests. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test, { after, before, describe } from "node:test";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { createServer } from "../src/index.js";
import { gitRun, gitTry } from "../src/git/exec.js";
import { cleanupTempDirs, isolateEnv, tempDir } from "./helpers.js";

isolateEnv();

let client: Client;
let store: string;

function texts(result: unknown): string[] {
  const content = (result as { content: Array<{ type: string; text?: string }> }).content;
  return content.filter((c) => c.type === "text").map((c) => c.text ?? "");
}

const call = (name: string, args: Record<string, unknown> = {}) => client.callTool({ name, arguments: args });

before(async () => {
  store = path.join(tempDir("mnemo-e2e-"), "store");
  process.env.MNEMO_DIR = store;
  process.env.MNEMO_MACHINE = "e2e-box";
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  client = new Client({ name: "mnemo-e2e", version: "0.0.0" });
  await Promise.all([createServer().connect(serverTransport), client.connect(clientTransport)]);
});

after(async () => {
  await client.close();
  cleanupTempDirs();
});

describe("a full save, end to end", () => {
  test("bootstrap provisions a store that did not exist", async () => {
    const [out] = texts(await call("mnemo_bootstrap"));
    assert.match(out!, /Created the store at/);
    assert.ok(fs.existsSync(path.join(store, "shared", "SCHEMA.md")));
    // The identity the rest of the flow commits under.
    gitRun(store, ["config", "user.name", "E2E Person"]);
    gitRun(store, ["config", "user.email", "e2e@example.invalid"]);
  });

  test("a memory cannot be written before its project exists", async () => {
    const result = await call("mnemo_write_memory", {
      id: "orion-auth-rotation", projects: ["orion-api"], type: "decision", body: "Tokens rotate every 15 minutes.",
    });
    assert.equal((result as { isError?: boolean }).isError, true);
    assert.match(texts(result)[0]!, /No project\(s\) 'orion-api'/);
  });

  test("project, memory and pending write, then one commit", async () => {
    assert.match(texts(await call("mnemo_upsert_project", {
      slug: "orion-api", name: "Orion API", services: ["api"], description: "The public API.",
    }))[0]!, /Created project 'orion-api'/);

    assert.match(texts(await call("mnemo_write_memory", {
      id: "orion-auth-rotation", projects: ["orion-api"], type: "decision",
      body: "Tokens rotate every 15 minutes.",
    }))[0]!, /Wrote memory 'orion-auth-rotation'.*Not committed yet/);

    assert.match(texts(await call("mnemo_write_pending", {
      slug: "orion-api",
      content: "# Pending — Orion API\n\n## In progress\n- [ ] Roll out rotation [@e2e-box]\n\n## Next\n- [ ] Audit backfill\n",
    }))[0]!, /Rewrote pending.md/);

    const [commit] = texts(await call("mnemo_commit", { message: "save(orion-api): rotation" }));
    assert.match(commit!, /Committed [0-9a-f]{7,}/);
    assert.match(commit!, /memories\/orion-auth-rotation\.md/);
    assert.match(commit!, /no remote, so it lives only on this machine/);
  });

  test("the saved work reads back as a resume card", async () => {
    const blocks = texts(await call("mnemo_load_project", { slug: "orion-api", lang: "en" }));
    assert.match(blocks[0]!, /📁 orion-api · active/);
    assert.match(blocks[0]!, /▶ Resume with: Roll out rotation \[@e2e-box\]/);
    assert.match(blocks[0]!, /⚖ Tokens rotate every 15 minutes\./);
    assert.ok(!blocks[0]!.includes("⚠"), "an item stamped for THIS machine carries no warning");
  });

  test("a second commit with nothing new is not an error", async () => {
    const [out] = texts(await call("mnemo_commit", { message: "save: nothing" }));
    assert.match(out!, /Nothing to commit/);
  });

  test("overwriting a memory needs saying so, and then keeps one commit history", async () => {
    const blocked = await call("mnemo_write_memory", {
      id: "orion-auth-rotation", projects: ["orion-api"], type: "decision", body: "Different.",
    });
    assert.equal((blocked as { isError?: boolean }).isError, true);
    assert.match(texts(blocked)[0]!, /overwrite: true/);

    await call("mnemo_write_memory", {
      id: "orion-auth-rotation", projects: ["orion-api"], type: "decision",
      body: "Tokens rotate every 15 minutes; refresh tokens are single use.", overwrite: true,
    });
    await call("mnemo_commit", { message: "save(orion-api): refine rotation\n\nCo-Authored-By: Someone <a@b.c>" });
    const log = gitTry(store, ["log", "--pretty=%B"]) ?? "";
    assert.ok(!log.includes("Co-Authored-By"), "the store never carries coauthor trailers");
    assert.equal((gitTry(store, ["rev-list", "--count", "HEAD"]) ?? "").trim(), "2");
  });
});
