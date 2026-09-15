/** The mailbox and the memory tools over HTTP, which is what lets an agent on another
 * machine in. Real HTTP clients against a real server on a random loopback port. */

import assert from "node:assert/strict";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import test, { after, before, describe } from "node:test";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { startHttpServer, type RunningHttpServer } from "../src/http.js";
import { cleanupTempDirs, FIXTURE_STORE, isolateEnv, tempDir } from "./helpers.js";

isolateEnv();

const TOKEN = `test-${randomUUID()}`;
const BIN = path.resolve(import.meta.dirname, "..", "src", "bin.js");
const clients: Client[] = [];
let running: RunningHttpServer;
let dir: string;

const INITIALIZE = {
  jsonrpc: "2.0",
  id: 1,
  method: "initialize",
  params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "probe", version: "0" } },
};
const JSON_HEADERS = { "content-type": "application/json", accept: "application/json, text/event-stream" };

async function httpClient(product: string, agent?: string, token = TOKEN) {
  const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
  if (agent) headers["X-Mnemo-Agent"] = agent;
  const transport = new StreamableHTTPClientTransport(new URL(running.url), { requestInit: { headers } });
  const client = new Client({ name: product, version: "0.0.0" });
  await client.connect(transport);
  clients.push(client);
  return { client, transport };
}

async function stdioClient(product: string, agent: string): Promise<Client> {
  const env: Record<string, string> = {};
  for (const [key, value] of Object.entries(process.env)) if (value !== undefined) env[key] = value;
  env.MNEMO_AGENT = agent;
  const client = new Client({ name: product, version: "0.0.0" });
  await client.connect(new StdioClientTransport({ command: process.execPath, args: [BIN], env, stderr: "ignore" }));
  clients.push(client);
  return client;
}

async function call(client: Client, name: string, args: Record<string, unknown> = {}) {
  const result = (await client.callTool({ name, arguments: args })) as { content: Array<{ text?: string }>; isError?: boolean };
  return { text: result.content.map((c) => c.text ?? "").join("\n"), isError: result.isError === true };
}

async function eventually(check: () => Promise<boolean>, ms = 3_000): Promise<void> {
  const deadline = Date.now() + ms;
  while (!(await check())) {
    if (Date.now() > deadline) throw new Error("condition not reached in time");
    await new Promise((r) => setTimeout(r, 100));
  }
}

before(async () => {
  dir = tempDir("mnemo-http-");
  process.env.MNEMO_MAILBOX_DIR = dir;
  process.env.MNEMO_DIR = FIXTURE_STORE;
  running = await startHttpServer({ token: TOKEN, port: 0 });
});

after(async () => {
  for (const client of clients) await client.close().catch(() => {});
  await running.close();
  cleanupTempDirs();
});

describe("exposure", () => {
  test("binds loopback by default", () => {
    assert.match(running.url, /^http:\/\/127\.0\.0\.1:\d+\/mcp$/);
  });

  test("refuses to start without a token", async () => {
    await assert.rejects(() => startHttpServer({ token: "  ", port: 0 }), /needs a token: set MNEMO_TOKEN/);
  });

  test("the executable refuses too, and rejects unknown flags", () => {
    const env = { ...process.env };
    delete env.MNEMO_TOKEN;
    const noToken = spawnSync(process.execPath, [BIN, "--http", "--port", "0"], { encoding: "utf8", env, timeout: 15_000 });
    assert.equal(noToken.status, 1);
    assert.match(noToken.stderr, /needs a token/);
    const bogus = spawnSync(process.execPath, [BIN, "--bogus"], { encoding: "utf8", env, timeout: 15_000 });
    assert.equal(bogus.status, 1);
    assert.match(bogus.stderr, /unknown argument: --bogus/);
  });

  test("no token or a wrong one gets 401, before any session exists", async () => {
    const none = await fetch(running.url, { method: "POST", headers: JSON_HEADERS, body: JSON.stringify(INITIALIZE) });
    assert.equal(none.status, 401);
    assert.match(none.headers.get("www-authenticate") ?? "", /Bearer/);
    const wrong = await fetch(running.url, {
      method: "POST",
      headers: { ...JSON_HEADERS, authorization: "Bearer not-the-token" },
      body: JSON.stringify(INITIALIZE),
    });
    assert.equal(wrong.status, 401);
    await assert.rejects(() => httpClient("codex", undefined, "not-the-token"));
  });

  test("malformed traffic is refused with the right status", async () => {
    const auth = { ...JSON_HEADERS, authorization: `Bearer ${TOKEN}` };
    const notInit = await fetch(running.url, { method: "POST", headers: auth, body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" }) });
    assert.equal(notInit.status, 400);
    const unknown = await fetch(running.url, { method: "POST", headers: { ...auth, "mcp-session-id": "nope" }, body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" }) });
    assert.equal(unknown.status, 404);
    const garbage = await fetch(running.url, { method: "POST", headers: auth, body: "{not json" });
    assert.equal(garbage.status, 400);
    const elsewhere = await fetch(running.url.replace(/\/mcp$/, "/other"), { headers: auth });
    assert.equal(elsewhere.status, 404);
  });
});

describe("an authorised session", () => {
  test("gets the whole surface: memory and mailbox", async () => {
    const { client } = await httpClient("claude-code");
    const names = (await client.listTools()).tools.map((t) => t.name);
    for (const tool of ["mnemo_status", "mnemo_load_project", "mailbox_send", "mailbox_wait"]) assert.ok(names.includes(tool), tool);
    assert.match((await call(client, "mnemo_status", { slug: "orion-api" })).text, /project: orion-api · active/);
  });
});

describe("identity over HTTP", () => {
  let front: Client;
  let back: Client;

  before(async () => {
    front = (await httpClient("claude-code", "laptop-front")).client;
    back = (await httpClient("codex", "vps-backend")).client;
    await call(front, "mailbox_inbox");
    await call(back, "mailbox_inbox");
  });

  test("the X-Mnemo-Agent header names the session; the product comes from the handshake", async () => {
    const { text } = await call(front, "mailbox_peers");
    assert.match(text, /\| laptop-front \(you\) \| claude-code \| live \|/);
    assert.match(text, /\| vps-backend \| codex \| live \|/);
  });

  test("a task crosses between two HTTP sessions and the done comes back", async () => {
    await call(front, "mailbox_send", { to: "vps-backend", type: "task", body: "Rotate the API keys.", project: "orion-api" });
    const received = await call(back, "mailbox_wait", { timeout_ms: 5_000 });
    const id = /\[(m[0-9a-z]+)\] task from laptop-front/.exec(received.text)?.[1];
    assert.ok(id, received.text);
    assert.equal((await call(back, "mailbox_reply", { id, type: "done", body: "Rotated." })).isError, false);
    const back2 = await call(front, "mailbox_wait", { timeout_ms: 5_000 });
    assert.match(back2.text, new RegExp(`done from vps-backend \\(codex\\)[\\s\\S]*in reply to ${id}`));
  });

  test("a session without the header is reachable at a short session address", async () => {
    const anon = (await httpClient("opencode")).client;
    const who = await call(anon, "mailbox_inbox");
    const address = /You are (session:[0-9a-f]{12}) \(opencode\)/.exec(who.text)?.[1];
    assert.ok(address, who.text);
    await call(front, "mailbox_send", { to: address, type: "note", body: "hello, unnamed" });
    assert.match((await call(anon, "mailbox_inbox")).text, /hello, unnamed/);
  });

  test("a second session asking for a taken name is told so", async () => {
    const twin = (await httpClient("codex", "vps-backend")).client;
    assert.match((await call(twin, "mailbox_peers")).text, /⚠ 'vps-backend' is held by another open chat/);
  });

  test("closing a session releases its name at once", async () => {
    const temp = await httpClient("codex", "temp-worker");
    await call(temp.client, "mailbox_inbox");
    await temp.transport.terminateSession();
    await temp.client.close();
    await eventually(async () => /\| temp-worker \| codex \| offline \|/.test((await call(front, "mailbox_peers")).text));

    const successor = (await httpClient("codex", "temp-worker")).client;
    assert.match((await call(successor, "mailbox_inbox")).text, /You are temp-worker \(codex\)/);
  });
});

describe("stdio and HTTP share one mailbox", () => {
  test("a task from an HTTP session reaches a local stdio chat, and the done comes back", async () => {
    const remote = (await httpClient("claude-code", "remote-lead")).client;
    const local = await stdioClient("codex", "local-worker");
    await call(remote, "mailbox_inbox");
    await call(local, "mailbox_inbox");

    await call(remote, "mailbox_send", { to: "local-worker", type: "task", body: "Run the migrations." });
    const received = await call(local, "mailbox_wait", { timeout_ms: 5_000 });
    const id = /\[(m[0-9a-z]+)\] task from remote-lead \(claude-code\)/.exec(received.text)?.[1];
    assert.ok(id, received.text);

    await call(local, "mailbox_reply", { id, type: "done", body: "Migrated." });
    assert.match((await call(remote, "mailbox_wait", { timeout_ms: 5_000 })).text, /done from local-worker \(codex\)/);
  });
});
