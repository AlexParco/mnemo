/** The mailbox's state machine, called directly. The cases are the ones P8 exists
 * for: a message that waits for someone who is not there yet, two chats of the same
 * product on one machine, a reply that closes exactly one thing. */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test, { after, describe } from "node:test";
import { HTTP_IDLE_MS, inbox, peers, readMessages, register, releaseSession, reply, send, wait, whoami, RETENTION_MS, type Caller } from "../src/mailbox/store.js";
import { cleanupTempDirs, isolateEnv, tempDir } from "./helpers.js";

isolateEnv();
after(cleanupTempDirs);

/** A pid that cannot be running: stands in for a chat whose process has exited. */
const DEAD_PID = 2147483646;

const caller = (key: string, extra: Partial<Caller> = {}): Caller => ({
  sessionKey: key,
  product: "claude-code",
  transport: "stdio",
  ...extra,
});
const box = () => tempDir("mnemo-mailbox-");
const bodies = (r: { messages: Array<{ body: string }> }) => r.messages.map((m) => m.body);

const rejects = (fn: () => Promise<unknown>, pattern: RegExp) =>
  assert.rejects(fn, (err: Error) => {
    assert.equal(err.name, "StoreError");
    assert.match(err.message, pattern);
    return true;
  });

describe("durable names", () => {
  test("a message to a name waits for whoever takes the name later", async () => {
    const dir = box();
    const vps = caller("vps", { configuredName: "vps-backend" });
    const sent = await send(dir, vps, { to: "laptop-front", type: "task", body: "fix checkout" });
    assert.match(sent.warning ?? "", /Nobody has registered 'laptop-front'/);

    const laptop = caller("laptop");
    assert.equal((await inbox(dir, laptop)).messages.length, 0, "an unnamed chat does not get it");
    await register(dir, laptop, "laptop-front");
    const got = await inbox(dir, laptop);
    assert.deepEqual(bodies(got), ["fix checkout"]);
    assert.equal(got.messages[0]!.from, "vps-backend");
    assert.equal((await inbox(dir, laptop)).messages.length, 0, "reading marks it read");
  });

  test("two chats of the same product on one machine are addressed separately", async () => {
    const dir = box();
    const a = caller("chat-a");
    const b = caller("chat-b");
    const sender = caller("sender", { product: "codex", configuredName: "vps" });
    await register(dir, a, "front");
    await register(dir, b, "api");
    await send(dir, sender, { to: "front", type: "note", body: "for a" });
    await send(dir, sender, { to: "api", type: "note", body: "for b" });
    assert.deepEqual(bodies(await inbox(dir, a)), ["for a"]);
    assert.deepEqual(bodies(await inbox(dir, b)), ["for b"]);
  });

  test("a name held by an open chat is refused, by registration and by configuration", async () => {
    const dir = box();
    await register(dir, caller("holder"), "front");
    await rejects(() => register(dir, caller("other"), "front"), /held by another open chat[\s\S]*Taken: front/);
    const configured = await whoami(dir, caller("cfg", { configuredName: "front" }));
    assert.equal(configured.name, null);
    assert.equal(configured.address, "session:cfg", "it stays reachable at its session address");
    assert.match(configured.conflict ?? "", /held by another open chat/);
  });

  test("a name is freed when the chat holding it has exited", async () => {
    const dir = box();
    await register(dir, caller("ghost", { pid: DEAD_PID }), "api");
    const successor = await register(dir, caller("successor"), "api");
    assert.equal(successor.name, "api");
  });

  test("a chat that restarts resumes from its name's cursor, not from the start", async () => {
    const dir = box();
    const sender = caller("sender", { configuredName: "sender" });
    const first = caller("first", { pid: DEAD_PID, configuredName: "front" });
    await whoami(dir, first);
    await send(dir, sender, { to: "front", type: "note", body: "one" });
    assert.deepEqual(bodies(await inbox(dir, first)), ["one"]);

    const restarted = caller("second", { configuredName: "front" });
    assert.deepEqual(bodies(await inbox(dir, restarted)), [], "already-read messages are not replayed");
    await send(dir, sender, { to: "front", type: "note", body: "two" });
    assert.deepEqual(bodies(await inbox(dir, restarted)), ["two"]);
  });

  test("an unnamed chat is still reachable at its session address", async () => {
    const dir = box();
    const anon = caller("anon");
    const me = await whoami(dir, anon);
    assert.equal(me.name, null);
    assert.equal(me.address, "session:anon");
    await send(dir, caller("s", { configuredName: "s" }), { to: "session:anon", type: "note", body: "hi" });
    assert.deepEqual(bodies(await inbox(dir, anon)), ["hi"]);
  });
});

describe("HTTP sessions", () => {
  // Over HTTP every session shares the server's pid, so liveness cannot come from
  // the process. These are the two ways an HTTP session lets go of its name.

  test("closing one releases its name at once", async () => {
    const dir = box();
    await register(dir, caller("remote", { transport: "http" }), "vps-backend");
    await rejects(() => register(dir, caller("other"), "vps-backend"), /held by another open chat/);
    await releaseSession(dir, "remote");
    assert.equal((await register(dir, caller("other"), "vps-backend")).name, "vps-backend");
  });

  test("one idle past the limit lets its name go, while messages to the name still wait", async () => {
    const dir = box();
    await register(dir, caller("remote", { transport: "http" }), "vps-backend");
    await send(dir, caller("s", { configuredName: "sender" }), { to: "vps-backend", type: "note", body: "still here" });

    const statePath = path.join(dir, "state.json");
    const state = JSON.parse(fs.readFileSync(statePath, "utf8")) as { sessions: Record<string, { lastSeen: string }> };
    state.sessions["remote"]!.lastSeen = new Date(Date.now() - HTTP_IDLE_MS - 60_000).toISOString();
    fs.writeFileSync(statePath, JSON.stringify(state));

    const successor = caller("successor", { configuredName: "vps-backend" });
    assert.equal((await whoami(dir, successor)).name, "vps-backend");
    assert.deepEqual(bodies(await inbox(dir, successor)), ["still here"]);
  });

  test("an idle stdio session is not expired: its process is the evidence", async () => {
    const dir = box();
    await register(dir, caller("local"), "front");
    const statePath = path.join(dir, "state.json");
    const state = JSON.parse(fs.readFileSync(statePath, "utf8")) as { sessions: Record<string, { lastSeen: string }> };
    state.sessions["local"]!.lastSeen = new Date(Date.now() - HTTP_IDLE_MS - 60_000).toISOString();
    fs.writeFileSync(statePath, JSON.stringify(state));
    await rejects(() => register(dir, caller("other"), "front"), /held by another open chat/);
  });
});

describe("groups", () => {
  test("reach everyone but the sender, by product, and never names that arrive later", async () => {
    const dir = box();
    const a = caller("a", { configuredName: "front" });
    const b = caller("b", { product: "codex", configuredName: "api" });
    const c = caller("c", { configuredName: "docs" });
    for (const who of [a, b, c]) await whoami(dir, who);

    await send(dir, a, { to: "@all", type: "note", body: "deploy at 5" });
    await send(dir, a, { to: "@codex", type: "note", body: "codex only" });

    assert.deepEqual(bodies(await inbox(dir, b)), ["deploy at 5", "codex only"]);
    assert.deepEqual(bodies(await inbox(dir, c)), ["deploy at 5"]);
    assert.deepEqual(bodies(await inbox(dir, a)), [], "the sender does not hear its own broadcast");
    assert.deepEqual(bodies(await inbox(dir, caller("late", { configuredName: "late" }))), []);
  });
});

describe("replies", () => {
  test("close exactly the message they name, once, and only from a recipient", async () => {
    const dir = box();
    const lead = caller("lead", { configuredName: "lead" });
    const worker = caller("worker", { product: "codex", configuredName: "worker" });
    const bystander = caller("by", { configuredName: "by" });
    for (const who of [lead, worker, bystander]) await whoami(dir, who);

    const task = (await send(dir, lead, { to: "worker", type: "task", body: "run the suite", project: "orion-api" })).message;
    const question = (await send(dir, lead, { to: "worker", type: "question", body: "which branch?" })).message;

    await rejects(() => reply(dir, worker, { id: question.id, type: "done", body: "x" }), /A 'done' closes a 'task'/);
    await rejects(() => reply(dir, bystander, { id: task.id, type: "done", body: "x" }), /not to you/);
    await rejects(() => reply(dir, worker, { id: "m-nope", type: "done", body: "x" }), /No message 'm-nope'/);
    await rejects(() => reply(dir, worker, { id: task.id, type: "note", body: "x" }), /neither/);

    await reply(dir, worker, { id: task.id, type: "done", body: "222 passing" });
    await rejects(() => reply(dir, worker, { id: task.id, type: "done", body: "again" }), /already sent a 'done'/);

    const back = await inbox(dir, lead);
    assert.deepEqual(
      back.messages.map((m) => [m.type, m.from, m.replyTo, m.project]),
      [["done", "worker", task.id, "orion-api"]],
      "the reply goes to the sender, bound to the task, carrying its project",
    );
  });
});

describe("send refuses", () => {
  test("what the contract forbids", async () => {
    const dir = box();
    const me = caller("me", { configuredName: "me" });
    await rejects(() => send(dir, me, { to: "x", type: "done", body: "b" }), /use mailbox_reply/);
    await rejects(() => send(dir, me, { to: "x", type: "shout", body: "b" }), /not a message type/);
    await rejects(() => send(dir, me, { to: "x", type: "note", body: "   " }), /needs a body/);
    await rejects(() => send(dir, me, { to: "me", type: "note", body: "b" }), /is you/);
    await rejects(() => send(dir, me, { to: "session:gone", type: "note", body: "b" }), /not an open chat/);
    await rejects(() => send(dir, me, { to: "Bad Name", type: "note", body: "b" }), /not a valid address/);
    await rejects(() => send(dir, me, { to: "x", type: "note", body: "b", project: "Not A Slug" }), /not a project slug/);
  });
});

describe("peers", () => {
  test("report who is live, who is gone, and what waits for each", async () => {
    const dir = box();
    const front = caller("a", { configuredName: "front" });
    await whoami(dir, front);
    await whoami(dir, caller("g", { pid: DEAD_PID, configuredName: "old" }));
    await whoami(dir, caller("anon", { product: "opencode" }));
    await send(dir, front, { to: "old", type: "note", body: "waiting" });

    const { peers: list } = await peers(dir, front);
    const by = (address: string) => list.find((p) => p.address === address);
    assert.equal(by("front")?.status, "live");
    assert.equal(by("front")?.you, true);
    assert.equal(list[0]!.address, "front", "you are listed first");
    assert.equal(by("old")?.status, "offline", "a gone process is never reported live");
    assert.equal(by("old")?.unread, 1);
    assert.equal(by("session:anon")?.product, "opencode");
  });
});

describe("retention", () => {
  test("delivered messages expire after 30 days; undelivered ones to a name do not; seq is never reused", async () => {
    const dir = box();
    fs.mkdirSync(dir, { recursive: true });
    const old = new Date(Date.now() - RETENTION_MS - 60_000).toISOString();
    const seed = [
      { seq: 1, id: "m1aaaa", ts: old, from: "vps", fromProduct: "codex", to: "front", type: "note", body: "read later" },
      { seq: 2, id: "m2aaaa", ts: old, from: "vps", fromProduct: "codex", to: "absent", type: "note", body: "never read" },
      { seq: 3, id: "m3aaaa", ts: old, from: "vps", fromProduct: "codex", to: "@all", type: "note", body: "old broadcast" },
    ];
    fs.writeFileSync(path.join(dir, "messages.jsonl"), seed.map((m) => JSON.stringify(m) + "\n").join(""));

    const front = caller("front", { configuredName: "front" });
    await inbox(dir, front);
    assert.deepEqual(readMessages(dir).map((m) => m.seq), [1, 2], "the old broadcast goes on the first pass");

    // Make the daily prune due again, now that front has read message 1.
    const statePath = path.join(dir, "state.json");
    const state = JSON.parse(fs.readFileSync(statePath, "utf8")) as { prunedAt?: string };
    state.prunedAt = old;
    fs.writeFileSync(statePath, JSON.stringify(state));
    await whoami(dir, front);
    assert.deepEqual(readMessages(dir).map((m) => m.seq), [2], "delivered goes; undelivered to a name stays");

    const next = await send(dir, front, { to: "absent", type: "note", body: "new" });
    assert.equal(next.message.seq, 4, "a pruned seq is never handed out again");
  });

  test("a torn last line from a crash does not corrupt the next message", async () => {
    const dir = box();
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, "messages.jsonl"), '{"seq":1,"id":"m1","ts":"2026-');
    await send(dir, caller("s", { configuredName: "s" }), { to: "r", type: "note", body: "after the crash" });
    assert.deepEqual(readMessages(dir).map((m) => m.body), ["after the crash"]);
  });
});

describe("wait", () => {
  test("returns as soon as a message lands", async () => {
    const dir = box();
    const waiter = caller("w", { configuredName: "waiter" });
    const sender = caller("s", { configuredName: "sender" });
    await whoami(dir, waiter);
    const started = Date.now();
    setTimeout(() => void send(dir, sender, { to: "waiter", type: "note", body: "ping" }), 300);
    const got = await wait(dir, waiter, 5_000);
    assert.equal(got.timedOut, false);
    assert.deepEqual(bodies(got), ["ping"]);
    assert.ok(Date.now() - started < 4_000, "it did not sit out the whole timeout");
  });

  test("times out empty, and stops when the request is cancelled", async () => {
    const dir = box();
    const waiter = caller("w", { configuredName: "waiter" });
    const empty = await wait(dir, waiter, 300);
    assert.equal(empty.timedOut, true);
    assert.equal(empty.messages.length, 0);

    const controller = new AbortController();
    setTimeout(() => controller.abort(), 200);
    const started = Date.now();
    const cancelled = await wait(dir, waiter, 10_000_000, controller.signal);
    assert.equal(cancelled.timedOut, true);
    assert.ok(Date.now() - started < 2_000, "a cancelled wait returns promptly");
  });
});
