/** The mailbox between real server processes. Over stdio every chat spawns its own
 * mnemo process, so two chats on one machine meet only through the state files and
 * the lock — which is what this exercises, through real MCP clients whose
 * `clientInfo` names the product. */

import assert from "node:assert/strict";
import path from "node:path";
import test, { after, before, describe } from "node:test";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { readMessages } from "../src/mailbox/store.js";
import { cleanupTempDirs, FIXTURE_STORE, isolateEnv, tempDir } from "./helpers.js";

isolateEnv();

const BIN = path.resolve(import.meta.dirname, "..", "src", "bin.js");
const open: Client[] = [];
let dir: string;

async function connect(product: string, agent?: string): Promise<Client> {
  const env: Record<string, string> = {};
  for (const [key, value] of Object.entries(process.env)) if (value !== undefined) env[key] = value;
  env.MNEMO_MAILBOX_DIR = dir;
  env.MNEMO_DIR = FIXTURE_STORE;
  if (agent) env.MNEMO_AGENT = agent;
  const client = new Client({ name: product, version: "0.0.0" });
  await client.connect(new StdioClientTransport({ command: process.execPath, args: [BIN], env, stderr: "ignore" }));
  open.push(client);
  return client;
}

async function call(client: Client, name: string, args: Record<string, unknown> = {}): Promise<{ text: string; isError: boolean }> {
  const result = (await client.callTool({ name, arguments: args })) as { content: Array<{ text?: string }>; isError?: boolean };
  return { text: result.content.map((c) => c.text ?? "").join("\n"), isError: result.isError === true };
}

before(() => {
  dir = tempDir("mnemo-mailbox-e2e-");
});

after(async () => {
  for (const client of open) await client.close().catch(() => {});
  cleanupTempDirs();
});

describe("two mnemo processes, one mailbox", () => {
  let lead: Client;
  let worker: Client;

  before(async () => {
    lead = await connect("claude-code", "laptop-front");
    worker = await connect("codex", "vps-backend");
    await call(worker, "mailbox_inbox"); // the first call claims the configured name
  });

  test("a task crosses from one process to the other, and the done comes back", async () => {
    const sent = await call(lead, "mailbox_send", {
      to: "vps-backend",
      type: "task",
      body: "The tariff endpoint returns 500 with an expired coupon.",
      project: "orion-api",
    });
    assert.equal(sent.isError, false, sent.text);

    const received = await call(worker, "mailbox_wait", { timeout_ms: 5_000 });
    assert.match(received.text, /task from laptop-front \(claude-code\) to vps-backend/);
    assert.match(received.text, /project: orion-api/);
    const id = /\[(m[0-9a-z]+)\] task/.exec(received.text)?.[1];
    assert.ok(id, received.text);

    const closed = await call(worker, "mailbox_reply", { id, type: "done", body: "Fixed: the coupon check now runs before pricing." });
    assert.equal(closed.isError, false, closed.text);

    const back = await call(lead, "mailbox_wait", { timeout_ms: 5_000 });
    assert.match(back.text, /done from vps-backend \(codex\) to laptop-front/);
    assert.match(back.text, new RegExp(`in reply to ${id}`));
  });

  test("the product each peer shows comes from its MCP handshake", async () => {
    const { text } = await call(lead, "mailbox_peers");
    assert.match(text, /\| laptop-front \(you\) \| claude-code \| live \|/);
    assert.match(text, /\| vps-backend \| codex \| live \|/);
  });

  test("a second chat configured with a taken name is told so, and can take another", async () => {
    const twin = await connect("codex", "vps-backend");
    const first = await call(twin, "mailbox_peers");
    assert.match(first.text, /⚠ 'vps-backend' is held by another open chat/);
    assert.match(first.text, /You are session:/);

    const renamed = await call(twin, "mailbox_register", { name: "vps-worker-2" });
    assert.equal(renamed.isError, false, renamed.text);
    assert.match(renamed.text, /You are vps-worker-2 \(codex\)/);
  });

  test("a pointer to a project that does not exist is refused", async () => {
    const result = await call(lead, "mailbox_send", { to: "vps-backend", type: "task", body: "x", project: "no-such-project" });
    assert.equal(result.isError, true);
    assert.match(result.text, /No project 'no-such-project'/);
  });

  test("both processes sending at once lose nothing and never share a seq", async () => {
    const before = readMessages(dir).length;
    const burst = (from: Client, to: string, n: number) =>
      Array.from({ length: n }, (_, i) => call(from, "mailbox_send", { to, type: "note", body: `${to} ${i}` }));
    const results = await Promise.all([...burst(lead, "vps-backend", 15), ...burst(worker, "laptop-front", 15)]);
    assert.ok(results.every((r) => !r.isError), results.find((r) => r.isError)?.text);

    const all = readMessages(dir);
    assert.equal(all.length, before + 30);
    assert.equal(new Set(all.map((m) => m.seq)).size, all.length, "every seq is unique");
    assert.equal(new Set(all.map((m) => m.id)).size, all.length, "every id is unique");
  });

  test("when a chat closes, its name shows offline and messages to it wait", async () => {
    await worker.close();
    let text = "";
    for (let i = 0; i < 20; i++) {
      text = (await call(lead, "mailbox_peers")).text;
      if (/\| vps-backend \| codex \| offline \|/.test(text)) break;
      await new Promise((r) => setTimeout(r, 100));
    }
    assert.match(text, /\| vps-backend \| codex \| offline \|/);

    const queued = await call(lead, "mailbox_send", { to: "vps-backend", type: "note", body: "for when you are back" });
    assert.equal(queued.isError, false);
    assert.doesNotMatch(queued.text, /Nobody has registered/, "a known name that is offline is not a typo");
  });
});
