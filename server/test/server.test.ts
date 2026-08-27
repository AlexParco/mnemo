/** End-to-end over the real MCP transport: the library being correct does not
 * prove the server is wired up. */

import assert from "node:assert/strict";
import test, { after, before, describe } from "node:test";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { createServer } from "../src/index.js";
import { FIXTURE_STORE } from "./helpers.js";

const EXPECTED_TOOLS = [
  "mnemo_status",
  "mnemo_list_projects",
  "mnemo_load_project",
  "mnemo_search_memories",
  "mnemo_read_memory",
];

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
  test("exposes exactly the read-only surface, all described", async () => {
    const { tools } = await client.listTools();
    assert.deepEqual(tools.map((t) => t.name).sort(), [...EXPECTED_TOOLS].sort());
    for (const tool of tools) {
      assert.ok((tool.description ?? "").length > 80, `${tool.name} needs a description agents can act on`);
      assert.equal(tool.annotations?.readOnlyHint, true, `${tool.name} must be marked read-only`);
    }
  });

  test("mnemo_list_projects renders the overview table", async () => {
    const [out] = texts(await client.callTool({ name: "mnemo_list_projects", arguments: {} }));
    assert.match(out!, /\| project \(slug\) \| status \| memories \| services \| updated \|/);
    assert.match(out!, /\| orion-api \| active \| 12 \(1 shared\) \|/);
    assert.match(out!, /4 projects · 15 memories/);
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
    assert.match(out!, /Existing slugs: atlas-web, odd-corners, orion-api, quiet-shed/);
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

  test("mnemo_status reports the store and this machine", async () => {
    const [out] = texts(await client.callTool({ name: "mnemo_status", arguments: {} }));
    assert.match(out!, /machine: fixture-box/);
    assert.match(out!, /projects: 4 · memories: 15/);
  });
});
