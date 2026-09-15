/** Waking a chat without being asked: the watcher announces, the chat reads.
 * What must hold is that watching changes nothing — no message marked read, no name
 * taken — and that each message is announced exactly once. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import test, { after, describe } from "node:test";
import { inbox, peekUnread, register, send, type Caller } from "../src/mailbox/store.js";
import { startHttpServer } from "../src/http.js";
import { cleanupTempDirs, isolateEnv, ROOT, tempDir } from "./helpers.js";

isolateEnv();

const BIN = path.resolve(import.meta.dirname, "..", "src", "bin.js");
const children: ChildProcess[] = [];
after(() => {
  for (const child of children) child.kill();
  cleanupTempDirs();
});

const caller = (key: string, extra: Partial<Caller> = {}): Caller => ({ sessionKey: key, product: "claude-code", transport: "stdio", ...extra });
const sessionKeys = (dir: string) => Object.keys((JSON.parse(fs.readFileSync(path.join(dir, "state.json"), "utf8")) as { sessions: object }).sessions).sort();

function startWatch(args: string[], env: Record<string, string>) {
  const child = spawn(process.execPath, [BIN, "watch", "--interval", "150", ...args], {
    env: { ...process.env, ...env },
    stdio: ["ignore", "pipe", "pipe"],
  });
  children.push(child);
  const lines: string[] = [];
  let stderr = "";
  child.stdout!.on("data", (chunk: Buffer) => lines.push(...chunk.toString().split("\n").filter(Boolean)));
  child.stderr!.on("data", (chunk: Buffer) => (stderr += chunk.toString()));
  return { child, lines, stderr: () => stderr };
}

async function until(check: () => boolean, ms = 4_000): Promise<void> {
  const deadline = Date.now() + ms;
  while (!check()) {
    if (Date.now() > deadline) throw new Error("condition not reached in time");
    await new Promise((r) => setTimeout(r, 50));
  }
}
const settle = (ms = 600) => new Promise((r) => setTimeout(r, ms));

describe("peekUnread", () => {
  test("shows what is waiting without marking it read or registering anyone", async () => {
    const dir = tempDir("mnemo-watch-");
    const sender = caller("sender", { configuredName: "sender" });
    await send(dir, sender, { to: "front", type: "task", body: "one" });
    const before = sessionKeys(dir);

    const peeked = peekUnread(dir, "front");
    assert.deepEqual(peeked.messages.map((m) => m.body), ["one"]);
    assert.deepEqual(peekUnread(dir, "front", peeked.lastSeq).messages, [], "after the last seq it saw, nothing new");
    assert.deepEqual(sessionKeys(dir), before, "peeking registered no session");

    const reader = caller("reader", { configuredName: "front" });
    assert.deepEqual((await inbox(dir, reader)).messages.map((m) => m.body), ["one"], "the chat still gets it");
    assert.deepEqual(peekUnread(dir, "front").messages, [], "and once read, it is no longer waiting");
  });

  test("groups reach only existing names, never the sender's own messages", async () => {
    const dir = tempDir("mnemo-watch-");
    await register(dir, caller("f"), "front");
    await send(dir, caller("s", { configuredName: "sender" }), { to: "@all", type: "note", body: "all" });
    await send(dir, caller("s", { configuredName: "sender" }), { to: "@codex", type: "note", body: "codex" });
    await send(dir, caller("f"), { to: "@all", type: "note", body: "mine" });
    assert.deepEqual(peekUnread(dir, "front", 0, "claude-code").messages.map((m) => m.body), ["all"]);
    assert.deepEqual(peekUnread(dir, "nobody").messages, [], "an unknown name gets no broadcasts");
  });
});

describe("mnemo-mcp watch, local mailbox", () => {
  test("announces each message once, as it arrives, and leaves it unread", async () => {
    const dir = tempDir("mnemo-watch-");
    const sender = caller("sender", { configuredName: "laptop-front" });
    await send(dir, sender, { to: "vps-backend", type: "task", body: "sent before the watcher started" });

    const w = startWatch(["--name", "vps-backend", "--server", "mnemo-vps"], { MNEMO_MAILBOX_DIR: dir });
    await until(() => w.lines.length === 1);
    assert.match(w.lines[0]!, /^\[mnemo\] task m[0-9a-z]+ from laptop-front: "sent before the watcher started" Read it with mailbox_inbox on the mnemo-vps server\.$/);

    await send(dir, sender, { to: "vps-backend", type: "question", body: "and one after" });
    await until(() => w.lines.length === 2);
    await settle();
    assert.equal(w.lines.length, 2, "no message is announced twice");
    assert.match(w.lines[1]!, /question .* "and one after"/);

    const chat = caller("chat", { configuredName: "vps-backend" });
    const read = await inbox(dir, chat);
    assert.deepEqual(read.me.name, "vps-backend", "the watcher never took the name");
    assert.deepEqual(read.messages.map((m) => m.body), ["sent before the watcher started", "and one after"]);
  });

  test("with no name it leaves quietly", () => {
    const res = spawnSync(process.execPath, [BIN, "watch"], { encoding: "utf8", env: { ...process.env }, timeout: 10_000 });
    assert.equal(res.status, 0);
    assert.equal(res.stdout, "");
    assert.match(res.stderr, /no name to watch/);
  });
});

describe("mnemo-mcp watch, remote server", () => {
  test("announces through the read-only endpoint, and survives a wrong token without noise", async () => {
    const dir = tempDir("mnemo-watch-http-");
    process.env.MNEMO_MAILBOX_DIR = dir;
    const token = `t-${randomUUID()}`;
    const running = await startHttpServer({ token, port: 0 });
    try {
      const good = startWatch(["--name", "vps-backend"], { MNEMO_URL: running.url, MNEMO_TOKEN: token });
      const bad = startWatch(["--name", "vps-backend"], { MNEMO_URL: running.url, MNEMO_TOKEN: "wrong" });
      await send(dir, caller("sender", { configuredName: "laptop-front" }), { to: "vps-backend", type: "task", body: "over the wire" });

      await until(() => good.lines.length === 1);
      assert.match(good.lines[0]!, /task .* from laptop-front: "over the wire"/);
      await settle();
      assert.equal(bad.lines.length, 0, "a rejected watcher announces nothing");
      assert.equal(bad.stderr().match(/HTTP 401/g)?.length, 1, "and reports the failure once, not every poll");
      assert.equal(bad.child.exitCode, null, "it keeps running, as a monitor must");
    } finally {
      await running.close();
      delete process.env.MNEMO_MAILBOX_DIR;
    }
  });

  test("the endpoint needs the token and a valid name, and registers nobody", async () => {
    const dir = tempDir("mnemo-watch-http-");
    process.env.MNEMO_MAILBOX_DIR = dir;
    const token = `t-${randomUUID()}`;
    const running = await startHttpServer({ token, port: 0 });
    try {
      await send(dir, caller("sender", { configuredName: "sender" }), { to: "front", type: "note", body: "hi" });
      const before = sessionKeys(dir);
      const base = running.url.replace(/\/mcp$/, "/mailbox/unread");
      assert.equal((await fetch(`${base}?name=front`)).status, 401);
      const auth = { headers: { authorization: `Bearer ${token}` } };
      assert.equal((await fetch(`${base}?name=Bad%20Name`, auth)).status, 400);
      const ok = (await (await fetch(`${base}?name=front`, auth)).json()) as { messages: Array<{ body: string }> };
      assert.deepEqual(ok.messages.map((m) => m.body), ["hi"]);
      assert.deepEqual(sessionKeys(dir), before, "watching over HTTP registered no session");
    } finally {
      await running.close();
      delete process.env.MNEMO_MAILBOX_DIR;
    }
  });
});

describe("the plugin monitor", () => {
  test("is declared, points at an executable script, and exits quietly without a name", () => {
    const monitors = JSON.parse(fs.readFileSync(path.join(ROOT, "monitors", "monitors.json"), "utf8")) as Array<{ name: string; command: string }>;
    assert.deepEqual(monitors.map((m) => m.name), ["mailbox"]);
    assert.match(monitors[0]!.command, /scripts\/mnemo-watch\.sh/);
    const script = path.join(ROOT, "scripts", "mnemo-watch.sh");
    assert.ok((fs.statSync(script).mode & 0o111) !== 0, "the script must be executable");
    const res = spawnSync("sh", [script], { encoding: "utf8", env: { ...process.env, MNEMO_AGENT: "" }, timeout: 10_000 });
    assert.equal(res.status, 0);
    assert.equal(res.stdout, "");
  });
});
