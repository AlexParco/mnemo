/** The behaviour layer. Every rule here has to reach a host through at least one
 * channel, and the channels differ by client — so what is tested is that the
 * rules exist, and that the copies of them cannot drift apart. */

import assert from "node:assert/strict";
import test, { after, before, describe } from "node:test";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import { createServer } from "../src/index.js";
import { CONFIRM_NOTE, CONFLICT_RULE, MACHINE_RULE, SAVE_CRITERION, SERVER_INSTRUCTIONS, guideText } from "../src/prompts/criterion.js";
import { FIXTURE_STORE, isolateEnv } from "./helpers.js";

isolateEnv();

const EXPECTED_PROMPTS = ["save_context", "load_context", "mem", "sync_memory"];

let client: Client;

function texts(result: unknown): string[] {
  const content = (result as { content: Array<{ type: string; text?: string }> }).content;
  return content.filter((c) => c.type === "text").map((c) => c.text ?? "");
}

before(async () => {
  process.env.MNEMO_DIR = FIXTURE_STORE;
  process.env.MNEMO_MACHINE = "fixture-box";
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  client = new Client({ name: "mnemo-behaviour", version: "0.0.0" });
  await Promise.all([createServer().connect(serverTransport), client.connect(clientTransport)]);
});

after(async () => {
  await client.close();
});

describe("channel 1: the server's instructions", () => {
  // The only channel that reaches Codex, which implements neither prompts nor
  // resources but does read `instructions`.
  test("the handshake carries them", () => {
    assert.equal(client.getInstructions(), SERVER_INSTRUCTIONS);
  });

  test("they say what the server cannot decide", () => {
    assert.match(SERVER_INSTRUCTIONS, /cannot see this conversation/);
    assert.match(SERVER_INSTRUCTIONS, /one fact per memory/);
    assert.match(SERVER_INSTRUCTIONS, /already in the code or in git history/);
    assert.match(SERVER_INSTRUCTIONS, /Slugs are exact/);
    assert.ok(SERVER_INSTRUCTIONS.split("\n").length < 15, "paid for on every connection: keep it short");
  });
});

describe("channel 2: MCP prompts", () => {
  test("all of them are exposed and described", async () => {
    const { prompts } = await client.listPrompts();
    assert.deepEqual(prompts.map((p) => p.name).sort(), [...EXPECTED_PROMPTS].sort());
    for (const prompt of prompts) {
      assert.ok((prompt.description ?? "").length > 30, `${prompt.name} needs a description`);
    }
  });

  test("save_context carries the distillation criterion and the stamping test", async () => {
    const result = await client.getPrompt({ name: "save_context", arguments: { project: "orion-api" } });
    const text = result.messages.map((m) => (m.content as { text: string }).text).join("\n");
    assert.match(text, /orion-api/);
    assert.ok(text.includes(SAVE_CRITERION), "the criterion must be the shared one, not a paraphrase");
    assert.ok(text.includes(MACHINE_RULE));
    assert.match(text, /mnemo_commit/);
  });

  test("load_context says to print the card verbatim and not to guess a slug", async () => {
    const result = await client.getPrompt({ name: "load_context", arguments: { project: "atlas-web" } });
    const text = result.messages.map((m) => (m.content as { text: string }).text).join("\n");
    assert.match(text, /verbatim/);
    assert.match(text, /Do not guess/);
    assert.ok(text.includes(MACHINE_RULE));
  });

  test("sync_memory carries the merge rule, including when to stop", async () => {
    const result = await client.getPrompt({ name: "sync_memory", arguments: {} });
    const text = result.messages.map((m) => (m.content as { text: string }).text).join("\n");
    assert.ok(text.includes(CONFLICT_RULE));
    assert.match(text, /abort/);
  });
});

describe("channel 3: the rules snippet", () => {
  test("mnemo_guide names the file and returns the rules", async () => {
    const blocks = texts(await client.callTool({ name: "mnemo_guide", arguments: { target: "agents" } }));
    assert.match(blocks[0]!, /Paste the following into AGENTS\.md/);
    assert.equal(blocks[1], guideText());
  });

  test("the target only changes the wording, never the rules", async () => {
    const forCursor = texts(await client.callTool({ name: "mnemo_guide", arguments: { target: "cursor" } }));
    assert.match(forCursor[0]!, /\.cursor\/rules\/mnemo\.md/);
    assert.equal(forCursor[1], guideText());
  });

  test("it covers every rule the other channels carry", () => {
    const guide = guideText();
    for (const [name, rule] of Object.entries({ SAVE_CRITERION, MACHINE_RULE, CONFLICT_RULE, CONFIRM_NOTE })) {
      assert.ok(guide.includes(rule), `${name} is missing from the guide`);
    }
  });
});

describe("the copies cannot drift", () => {
  test("load_project returns the same machine rule the guide does", async () => {
    const blocks = texts(await client.callTool({ name: "mnemo_load_project", arguments: { slug: "orion-api" } }));
    assert.ok(blocks[2]!.includes(MACHINE_RULE), "the tool response must reuse the shared rule");
    assert.ok(guideText().includes(MACHINE_RULE));
  });

  test("the destructive tools return the same confirmation note the guide does", async () => {
    const blocks = texts(await client.callTool({ name: "mnemo_forget", arguments: { kind: "project", target: "quiet-shed" } }));
    assert.ok(blocks[2]!.includes(CONFIRM_NOTE));
    assert.ok(guideText().includes(CONFIRM_NOTE));
  });
});
