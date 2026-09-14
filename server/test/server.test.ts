/** End-to-end over the real MCP transport: the library being correct does not
 * prove the server is wired up. */

import assert from "node:assert/strict";
import test, { after, before, describe } from "node:test";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { createServer } from "../src/index.js";
import { FIXTURE_STORE } from "./helpers.js";

const READ_TOOLS = ["mnemo_status", "mnemo_list_projects", "mnemo_load_project", "mnemo_search_memories", "mnemo_read_memory"];
const WRITE_TOOLS = ["mnemo_bootstrap", "mnemo_upsert_project", "mnemo_write_memory", "mnemo_write_pending", "mnemo_commit"];
const SYNC_TOOLS = ["mnemo_sync", "mnemo_resolve_conflict", "mnemo_rebase", "mnemo_push"];
const DESTRUCTIVE_TOOLS = ["mnemo_rename", "mnemo_forget"];
// Read-only, but it reads the criterion rather than the store.
const META_TOOLS = ["mnemo_guide"];

let client: Client;
const savedEnv = { dir: process.env.MNEMO_DIR, machine: process.env.MNEMO_MACHINE };

function texts(result: unknown): string[] {
  const content = (result as { content: Array<{ type: string; text?: string }> }).content;
  return content.filter((c) => c.type === "text").map((c) => c.text ?? "");
}

before(async () => {
  process.env.MNEMO_DIR = FIXTURE_STORE;
  process.env.MNEMO_MACHINE = "fixture-box";
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  client = new Client({ name: "mnemo-test", version: "0.0.0" });
  await Promise.all([createServer().connect(serverTransport), client.connect(clientTransport)]);
});

after(async () => {
  await client.close();
  process.env.MNEMO_DIR = savedEnv.dir;
  process.env.MNEMO_MACHINE = savedEnv.machine;
});

describe("mnemo MCP server", () => {
  test("exposes the whole surface, described and correctly annotated", async () => {
    const { tools } = await client.listTools();
    assert.deepEqual(tools.map((t) => t.name).sort(), [...READ_TOOLS, ...WRITE_TOOLS, ...SYNC_TOOLS, ...DESTRUCTIVE_TOOLS, ...META_TOOLS].sort());
    for (const tool of tools) {
      // Outside Claude Code the description is the only place the rules live.
      assert.ok((tool.description ?? "").length > 80, `${tool.name} needs a description agents can act on`);
      assert.equal(
        tool.annotations?.readOnlyHint === true,
        [...READ_TOOLS, ...META_TOOLS].includes(tool.name),
        `${tool.name} is annotated with the wrong read-only hint`,
      );
    }
  });

  test("mnemo_list_projects renders the overview table", async () => {
    const [out] = texts(await client.callTool({ name: "mnemo_list_projects", arguments: {} }));
    assert.match(out!, /\| project \(slug\) \| status \| memories \| services \| updated \|/);
    assert.match(out!, /\| orion-api \| active \| 12 \(1 shared\) \|/);
    assert.match(out!, /5 projects · 15 memories/);
  });

  test("mnemo_load_project puts the card first and the rules last", async () => {
    const blocks = texts(await client.callTool({ name: "mnemo_load_project", arguments: { slug: "orion-api", lang: "en" } }));
    assert.equal(blocks.length, 3);
    assert.ok(blocks[0]!.startsWith("📁 orion-api · active"), "first block must be the card, printable verbatim");
    assert.match(blocks[1]!, /^Detail \(do not print unless asked\):/);
    assert.match(blocks[2]!, /This machine is 'fixture-box'/);
    assert.match(blocks[2]!, /Do not act on them here/);
    const detail = JSON.parse(blocks[1]!.split("\n").slice(1).join("\n"));
    assert.equal(detail.memories.length, 12);
    assert.ok(detail.shared.some((s: { name: string }) => s.name === "CONVENTIONS.md"));
  });

  test("the card language follows the request", async () => {
    const [es] = texts(await client.callTool({ name: "mnemo_load_project", arguments: { slug: "orion-api", lang: "es" } }));
    assert.match(es!, /Retomar por:/);
  });

  test("an unknown slug fails loudly and lists the real ones", async () => {
    const result = await client.callTool({ name: "mnemo_load_project", arguments: { slug: "not-a-project" } });
    assert.equal((result as { isError?: boolean }).isError, true);
    const [out] = texts(result);
    assert.match(out!, /Existing slugs: atlas-web, mixed-tongues, odd-corners, orion-api, quiet-shed/);
    assert.match(out!, /do not guess or invent a project/);
  });

  test("search finds a memory and read returns it whole", async () => {
    const [hits] = texts(await client.callTool({ name: "mnemo_search_memories", arguments: { query: "rate limit" } }));
    const parsed = JSON.parse(hits!) as Array<{ id: string }>;
    assert.deepEqual(parsed.map((m) => m.id), ["orion-rate-limit-invariant"]);
    const [raw] = texts(await client.callTool({ name: "mnemo_read_memory", arguments: { id: "orion-rate-limit-invariant" } }));
    assert.match(raw!, /^---\nid: orion-rate-limit-invariant/);
    assert.match(raw!, /20% of the global budget/);
  });

  test("a missing memory is an error, not an empty answer", async () => {
    const result = await client.callTool({ name: "mnemo_read_memory", arguments: { id: "does-not-exist" } });
    assert.equal((result as { isError?: boolean }).isError, true);
  });

  test("mnemo_status with a slug adds where that project stands, cheaply", async () => {
    const [out, hint] = texts(await client.callTool({ name: "mnemo_status", arguments: { slug: "orion-api" } }));
    assert.match(out!, /^store: /m, "the store block is still there");
    assert.match(out!, /project: orion-api · active/);
    assert.match(out!, /memories: 12 \(1 shared with other projects\)/);
    assert.match(out!, /pending: 6 open — 2 in progress, 4 next · 1 belongs to another machine/);
    assert.match(out!, /next: Finish the token rotation rollout/);
    assert.match(hint!, /mnemo_load_project/, "it should point at the full picture");

    // The point of the slug form: a re-check must not cost what a reload costs.
    const full = texts(await client.callTool({ name: "mnemo_load_project", arguments: { slug: "orion-api" } })).join("");
    assert.ok(out!.length * 5 < full.length, `status ${out!.length} vs load ${full.length}`);
  });

  test("mnemo_status without a slug stays store-only", async () => {
    const [out] = texts(await client.callTool({ name: "mnemo_status", arguments: {} }));
    assert.ok(!out!.includes("project: "), "no project block without a slug");
  });

  test("mnemo_status with an unknown slug says so and lists the real ones", async () => {
    const blocks = texts(await client.callTool({ name: "mnemo_status", arguments: { slug: "ghost" } }));
    assert.match(blocks[0]!, /^store: /m, "the store status still comes back");
    assert.match(blocks[1]!, /No project 'ghost'[\s\S]*Existing slugs: atlas-web/);
  });

  test("load_project detail:card drops the detail block, keeps the rule", async () => {
    const card = texts(await client.callTool({ name: "mnemo_load_project", arguments: { slug: "orion-api", detail: "card" } }));
    assert.equal(card.length, 2);
    assert.ok(card[0]!.startsWith("📁 orion-api"));
    assert.match(card[1]!, /This machine is 'fixture-box'/);
    assert.ok(!card.join("").includes("Detail (do not print unless asked)"));
    const full = texts(await client.callTool({ name: "mnemo_load_project", arguments: { slug: "orion-api" } }));
    assert.equal(full.length, 3, "full is still the default");
  });

  test("mnemo_status reports the store and this machine", async () => {
    const [out] = texts(await client.callTool({ name: "mnemo_status", arguments: {} }));
    assert.match(out!, /machine: fixture-box/);
    assert.match(out!, /projects: 5 · memories: 15/);
  });
});
