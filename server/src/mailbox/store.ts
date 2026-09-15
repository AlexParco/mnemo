/** The mailbox: messages between agents, on this machine or others, from any MCP client.
 *
 * State lives outside the memory store, under the state dir — messages are
 * ephemeral, memory is not. Two files:
 *
 *   messages.jsonl   append-only log; every message carries a stable `seq`
 *   state.json       sessions, names, cursors, the next seq
 *
 * Every operation runs under the same cross-process lock as the store, so two chats
 * on one machine — two stdio server processes — meet safely in these files.
 *
 * Addresses come in three kinds:
 *   name              durable: a message waits until whoever holds the name reads it
 *   session:<key>     ephemeral: one open chat, gone when it closes
 *   @all, @<product>  groups
 *
 * Cursors belong to addresses, not connections. A name has at most one live holder
 * (collisions are refused), so a chat that restarts and reclaims its name resumes
 * from the name's cursor. That is what makes delivery survive a restart. */

import fs from "node:fs";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { setTimeout as sleep } from "node:timers/promises";
import { withLock } from "../store/lock.js";
import { StoreError } from "../store/write.js";

export const MESSAGE_TYPES = ["task", "done", "question", "answer", "note"] as const;
export type MessageType = (typeof MESSAGE_TYPES)[number];
export const SEND_TYPES = ["task", "question", "note"] as const;
export type SendType = (typeof SEND_TYPES)[number];
export const REPLY_TYPES = ["done", "answer"] as const;
export type ReplyType = (typeof REPLY_TYPES)[number];

/** What each reply closes. A completion has to name the exact message it closes —
 * Orca's rule, so a stale retry cannot close the wrong piece of work. */
const CLOSES: Record<ReplyType, MessageType> = { done: "task", answer: "question" };

export const RETENTION_MS = 30 * 24 * 60 * 60 * 1000;
const PRUNE_EVERY_MS = 24 * 60 * 60 * 1000;
export const WAIT_DEFAULT_MS = 30_000;
/** Codex cuts a tool call at 60 s by default (`tool_timeout_sec`). */
export const WAIT_MAX_MS = 55_000;
const WAIT_POLL_MS = 250;

export const NAME_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;
const GROUP_RE = /^@[a-z0-9][a-z0-9-]*$/;
const PROJECT_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
export const SESSION_PREFIX = "session:";

export type Transport = "stdio" | "http";

export interface Message {
  seq: number;
  id: string;
  ts: string;
  from: string;
  fromProduct: string;
  to: string;
  type: MessageType;
  body: string;
  project?: string;
  replyTo?: string;
}

interface SessionEntry {
  key: string;
  product: string;
  transport: Transport;
  pid: number;
  /** Group messages up to this seq predate the session and are not delivered to it. */
  firstSeq: number;
  /** Last seq considered for this session's own address. */
  cursor: number;
  name: string | null;
  lastSeen: string;
}

interface NameEntry {
  name: string;
  product: string;
  /** The session holding the name now; null when nobody does. */
  holder: string | null;
  firstSeq: number;
  cursor: number;
  lastSeen: string;
}

interface State {
  nextSeq: number;
  sessions: Record<string, SessionEntry>;
  names: Record<string, NameEntry>;
  prunedAt?: string;
}

/** Who is calling. Built by the tool layer from the MCP request. */
export interface Caller {
  sessionKey: string;
  product: string;
  transport: Transport;
  /** A name asked for by configuration: MNEMO_AGENT over stdio, X-Mnemo-Agent over HTTP. */
  configuredName?: string;
  /** The process that owns the session; defaults to this one. */
  pid?: number;
}

/** The caller as the mailbox resolved it. */
export interface Me {
  address: string;
  name: string | null;
  sessionAddress: string;
  product: string;
  /** Why a configured name could not be taken, when it could not. */
  conflict?: string;
}

interface Resolved extends Me {
  floor: number;
  nameCursor: number;
  sessionCursor: number;
}

export interface Peer {
  address: string;
  name: string | null;
  product: string;
  /** `live`: the process holding it was confirmed running during this call.
   * `offline`: a known name nobody holds. Never inferred from stale state. */
  status: "live" | "offline";
  unread: number;
  lastSeen: string;
  you: boolean;
}

interface Ctx {
  dir: string;
  state: State;
  messages: Message[];
  now: string;
  session: SessionEntry;
  me: Resolved;
}

const messagesFile = (dir: string) => path.join(dir, "messages.jsonl");
const stateFile = (dir: string) => path.join(dir, "state.json");

// ---------------------------------------------------------------------- files

export function readMessages(dir: string): Message[] {
  let raw: string;
  try {
    raw = fs.readFileSync(messagesFile(dir), "utf8");
  } catch {
    return [];
  }
  const out: Message[] = [];
  for (const line of raw.split("\n")) {
    if (!line.trim()) continue;
    try {
      out.push(JSON.parse(line) as Message);
    } catch {
      // A torn line from a crash mid-write: skip it rather than lose the whole log.
    }
  }
  return out;
}

function readState(dir: string): State {
  try {
    const parsed = JSON.parse(fs.readFileSync(stateFile(dir), "utf8")) as Partial<State>;
    return {
      nextSeq: typeof parsed.nextSeq === "number" ? parsed.nextSeq : 1,
      sessions: parsed.sessions ?? {},
      names: parsed.names ?? {},
      ...(parsed.prunedAt ? { prunedAt: parsed.prunedAt } : {}),
    };
  } catch {
    return { nextSeq: 1, sessions: {}, names: {} };
  }
}

function writeState(dir: string, state: State): void {
  const tmp = `${stateFile(dir)}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(state, null, 2) + "\n");
  fs.renameSync(tmp, stateFile(dir));
}

function fileSize(file: string): number {
  try {
    return fs.statSync(file).size;
  } catch {
    return 0;
  }
}

/** Whether the log ends in a newline. If a crash left a torn last line, the next
 * append must start on a fresh line or it would corrupt the new message too. */
function endsWithNewline(file: string): boolean {
  let fd: number | undefined;
  try {
    fd = fs.openSync(file, "r");
    const size = fs.fstatSync(fd).size;
    if (size === 0) return true;
    const buf = Buffer.alloc(1);
    fs.readSync(fd, buf, 0, 1, size - 1);
    return buf[0] === 0x0a;
  } catch {
    return true;
  } finally {
    if (fd !== undefined) fs.closeSync(fd);
  }
}

// ------------------------------------------------------------------- sessions

function pidAlive(pid: number): boolean {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  if (pid === process.pid) return true;
  try {
    process.kill(pid, 0); // signal 0 only probes for existence
    return true;
  } catch (err) {
    return (err as NodeJS.ErrnoException).code === "EPERM";
  }
}

/** Drop sessions whose process is gone and free the names they held. Over stdio a
 * session is its process, so the pid is the whole liveness story. */
function reap(state: State): void {
  for (const [key, session] of Object.entries(state.sessions)) {
    if (!pidAlive(session.pid)) delete state.sessions[key];
  }
  for (const entry of Object.values(state.names)) {
    if (entry.holder && !state.sessions[entry.holder]) entry.holder = null;
  }
}

function claim(state: State, name: string, session: SessionEntry, now: string): { ok: true } | { ok: false; reason: string } {
  if (!NAME_RE.test(name)) {
    return { ok: false, reason: `'${name}' is not a valid name: lower-case letters, digits and dashes, starting with a letter or digit.` };
  }
  const existing = state.names[name];
  if (existing?.holder && existing.holder !== session.key && state.sessions[existing.holder]) {
    const taken = Object.values(state.names).filter((n) => n.holder).map((n) => n.name).sort();
    return {
      ok: false,
      reason: `'${name}' is held by another open chat (${existing.product}). Pick a different name with mailbox_register. Taken: ${taken.join(", ")}.`,
    };
  }
  if (session.name && session.name !== name) {
    const previous = state.names[session.name];
    if (previous && previous.holder === session.key) previous.holder = null;
  }
  const top = state.nextSeq - 1;
  state.names[name] = existing
    ? { ...existing, holder: session.key, product: session.product, lastSeen: now }
    : // A new name starts at cursor 0, so messages left for it before anyone took it
      // are delivered; group messages from before it existed are not.
      { name, product: session.product, holder: session.key, firstSeq: top, cursor: 0, lastSeen: now };
  session.name = name;
  return { ok: true };
}

function touch(state: State, caller: Caller, now: string): { session: SessionEntry; conflict?: string } {
  const top = state.nextSeq - 1;
  let session = state.sessions[caller.sessionKey];
  if (!session) {
    session = {
      key: caller.sessionKey,
      product: caller.product,
      transport: caller.transport,
      pid: caller.pid ?? process.pid,
      firstSeq: top,
      cursor: top,
      name: null,
      lastSeen: now,
    };
    state.sessions[session.key] = session;
  }
  session.product = caller.product;
  session.lastSeen = now;
  if (session.name && state.names[session.name]?.holder !== session.key) session.name = null;

  let conflict: string | undefined;
  if (!session.name && caller.configuredName) {
    const result = claim(state, caller.configuredName, session, now);
    if (!result.ok) conflict = result.reason;
  }
  if (session.name) state.names[session.name]!.lastSeen = now;
  return { session, ...(conflict ? { conflict } : {}) };
}

function resolve(state: State, session: SessionEntry, conflict?: string): Resolved {
  const sessionAddress = SESSION_PREFIX + session.key;
  const named = session.name ? state.names[session.name] : undefined;
  const name = named && named.holder === session.key ? named.name : null;
  return {
    address: name ?? sessionAddress,
    name,
    sessionAddress,
    product: session.product,
    ...(conflict ? { conflict } : {}),
    floor: name ? named!.firstSeq : session.firstSeq,
    nameCursor: name ? named!.cursor : session.cursor,
    sessionCursor: session.cursor,
  };
}

function publicMe(r: Resolved): Me {
  return {
    address: r.address,
    name: r.name,
    sessionAddress: r.sessionAddress,
    product: r.product,
    ...(r.conflict ? { conflict: r.conflict } : {}),
  };
}

// ------------------------------------------------------------------- delivery

function isForMe(m: Message, me: Resolved): boolean {
  if (m.from === me.address || m.from === me.sessionAddress) return false;
  if (m.to === me.sessionAddress) return m.seq > me.sessionCursor;
  if (m.seq <= me.nameCursor) return false;
  if (me.name !== null && m.to === me.name) return true;
  if (m.to === "@all" || m.to === `@${me.product}`) return m.seq > me.floor;
  return false;
}

/** Was this message sent to the caller at all — cursors aside. Who may close it. */
function wasAddressedTo(m: Message, me: Resolved): boolean {
  return m.to === me.sessionAddress || (me.name !== null && m.to === me.name) || m.to === "@all" || m.to === `@${me.product}`;
}

function append(ctx: Ctx, draft: Omit<Message, "seq" | "id" | "ts">): Message {
  const seq = ctx.state.nextSeq++;
  const message: Message = { seq, id: `m${seq.toString(36)}${randomBytes(2).toString("hex")}`, ts: ctx.now, ...draft };
  const file = messagesFile(ctx.dir);
  fs.appendFileSync(file, (endsWithNewline(file) ? "" : "\n") + JSON.stringify(message) + "\n");
  ctx.messages.push(message);
  return message;
}

function expired(m: Message, state: State, nowMs: number): boolean {
  const ts = Date.parse(m.ts);
  if (!Number.isFinite(ts) || nowMs - ts < RETENTION_MS) return false;
  // Groups and session addresses: nothing durable is waiting on them.
  if (m.to.startsWith("@") || m.to.startsWith(SESSION_PREFIX)) return true;
  // A name: kept for as long as whoever holds that name has not read it.
  const name = state.names[m.to];
  return name !== undefined && name.cursor >= m.seq;
}

function pruneIfDue(dir: string, state: State, messages: Message[], nowMs: number): Message[] {
  if (state.prunedAt && nowMs - Date.parse(state.prunedAt) < PRUNE_EVERY_MS) return messages;
  state.prunedAt = new Date(nowMs).toISOString();
  const keep = messages.filter((m) => !expired(m, state, nowMs));
  if (keep.length === messages.length) return messages;
  const tmp = `${messagesFile(dir)}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, keep.map((m) => JSON.stringify(m) + "\n").join(""));
  fs.renameSync(tmp, messagesFile(dir));
  return keep;
}

async function transact<T>(dir: string, caller: Caller, fn: (ctx: Ctx) => T): Promise<T> {
  fs.mkdirSync(dir, { recursive: true });
  return withLock(dir, () => {
    const nowMs = Date.now();
    const now = new Date(nowMs).toISOString();
    let messages = readMessages(dir);
    const state = readState(dir);
    // state.json can be lost or stale after a crash; the log is the truth for seq,
    // and a reused seq would let a reply bind to the wrong message.
    const maxSeq = messages.reduce((top, m) => Math.max(top, m.seq), 0);
    state.nextSeq = Math.max(state.nextSeq, maxSeq + 1);
    reap(state);
    messages = pruneIfDue(dir, state, messages, nowMs);
    const { session, conflict } = touch(state, caller, now);
    const result = fn({ dir, state, messages, now, session, me: resolve(state, session, conflict) });
    writeState(dir, state);
    return result;
  });
}

// ------------------------------------------------------------------ operations

export async function whoami(dir: string, caller: Caller): Promise<Me> {
  return transact(dir, caller, ({ me }) => publicMe(me));
}

export async function register(dir: string, caller: Caller, name: string): Promise<Me> {
  return transact(dir, caller, (ctx) => {
    const result = claim(ctx.state, name.trim(), ctx.session, ctx.now);
    if (!result.ok) throw new StoreError(result.reason);
    return publicMe(resolve(ctx.state, ctx.session));
  });
}

export interface SendInput {
  to: string;
  type: string;
  body: string;
  project?: string;
}

export interface SendResult {
  message: Message;
  me: Me;
  warning?: string;
}

export async function send(dir: string, caller: Caller, input: SendInput): Promise<SendResult> {
  return transact(dir, caller, (ctx) => {
    const { state, me } = ctx;
    if (!(SEND_TYPES as readonly string[]).includes(input.type)) {
      throw new StoreError(
        (REPLY_TYPES as readonly string[]).includes(input.type)
          ? `A '${input.type}' closes a specific message: use mailbox_reply with that message's id.`
          : `'${input.type}' is not a message type to send. Use one of: ${SEND_TYPES.join(", ")}.`,
      );
    }
    const body = input.body.trim();
    if (!body) throw new StoreError("A message needs a body.");
    const to = input.to.trim();
    if (to === me.address || to === me.sessionAddress) throw new StoreError("That address is you.");

    let warning: string | undefined;
    if (to.startsWith("@")) {
      if (!GROUP_RE.test(to)) throw new StoreError(`'${to}' is not a group. Use @all, or @<product> such as @codex.`);
    } else if (to.startsWith(SESSION_PREFIX)) {
      if (!state.sessions[to.slice(SESSION_PREFIX.length)]) {
        throw new StoreError(
          `${to} is not an open chat. Session addresses last only while the chat does; send to a name for anything that should wait.`,
        );
      }
    } else {
      if (!NAME_RE.test(to)) throw new StoreError(`'${to}' is not a valid address. See mailbox_peers for who you can reach.`);
      if (!state.names[to]) {
        warning = `Nobody has registered '${to}' yet. The message will wait for it — check the spelling with mailbox_peers.`;
      }
    }

    let project: string | undefined;
    if (input.project !== undefined) {
      project = input.project.trim();
      if (!PROJECT_RE.test(project)) throw new StoreError(`'${project}' is not a project slug.`);
    }

    const message = append(ctx, {
      from: me.address,
      fromProduct: me.product,
      to,
      type: input.type as SendType,
      body,
      ...(project ? { project } : {}),
    });
    return { message, me: publicMe(me), ...(warning ? { warning } : {}) };
  });
}

export interface ReplyInput {
  id: string;
  type: string;
  body: string;
}

export async function reply(dir: string, caller: Caller, input: ReplyInput): Promise<SendResult> {
  return transact(dir, caller, (ctx) => {
    const { me, messages, state } = ctx;
    if (!(REPLY_TYPES as readonly string[]).includes(input.type)) {
      throw new StoreError(
        `A reply is 'done' (closes a task) or 'answer' (answers a question); '${input.type}' is neither. To start something new, use mailbox_send.`,
      );
    }
    const type = input.type as ReplyType;
    const body = input.body.trim();
    if (!body) throw new StoreError("A reply needs a body: what was done, or the answer.");

    const id = input.id.trim();
    const original = messages.find((m) => m.id === id);
    if (!original) throw new StoreError(`No message '${id}'. Delivered messages are kept for 30 days.`);
    if (original.type !== CLOSES[type]) {
      throw new StoreError(`A '${type}' closes a '${CLOSES[type]}', and ${original.id} is a '${original.type}'.`);
    }
    if (!wasAddressedTo(original, me)) {
      throw new StoreError(`${original.id} was sent to ${original.to}, not to you (${me.address}). Only a recipient can close it.`);
    }
    if (messages.some((m) => m.replyTo === original.id && m.type === type && m.from === me.address)) {
      throw new StoreError(`You already sent a '${type}' for ${original.id}.`);
    }

    const to = original.from;
    let warning: string | undefined;
    if (to.startsWith(SESSION_PREFIX) && !state.sessions[to.slice(SESSION_PREFIX.length)]) {
      warning = `${to} has closed, and session addresses do not wait. The reply is recorded, but nobody will receive it.`;
    }
    const message = append(ctx, {
      from: me.address,
      fromProduct: me.product,
      to,
      type,
      body,
      replyTo: original.id,
      ...(original.project ? { project: original.project } : {}),
    });
    return { message, me: publicMe(me), ...(warning ? { warning } : {}) };
  });
}

export async function inbox(dir: string, caller: Caller): Promise<{ messages: Message[]; me: Me }> {
  return transact(dir, caller, (ctx) => {
    const { me, state, session } = ctx;
    const mine = ctx.messages.filter((m) => isForMe(m, me));
    const top = state.nextSeq - 1;
    session.cursor = top;
    if (me.name) state.names[me.name]!.cursor = top;
    return { messages: mine, me: publicMe(me) };
  });
}

/** Block until something arrives for the caller, or the timeout passes. Watches the
 * log's size and only takes the lock when it changed, so an idle wait costs a stat
 * every 250 ms. */
export async function wait(
  dir: string,
  caller: Caller,
  timeoutMs: number = WAIT_DEFAULT_MS,
  signal?: AbortSignal,
): Promise<{ messages: Message[]; me: Me; timedOut: boolean }> {
  const deadline = Date.now() + Math.min(Math.max(0, timeoutMs), WAIT_MAX_MS);
  let seenSize = -1;
  for (;;) {
    const size = fileSize(messagesFile(dir));
    if (size !== seenSize) {
      seenSize = size;
      const result = await inbox(dir, caller);
      if (result.messages.length > 0) return { ...result, timedOut: false };
    }
    const left = deadline - Date.now();
    if (left <= 0 || signal?.aborted) return { messages: [], me: await whoami(dir, caller), timedOut: true };
    try {
      await sleep(Math.min(WAIT_POLL_MS, left), undefined, signal ? { signal } : undefined);
    } catch {
      // aborted: the next pass returns
    }
  }
}

export async function peers(dir: string, caller: Caller): Promise<{ me: Me; peers: Peer[] }> {
  return transact(dir, caller, ({ state, messages, me, session }) => {
    const unreadFor = (address: string, cursor: number) => messages.filter((m) => m.to === address && m.seq > cursor).length;
    const list: Peer[] = [];
    for (const entry of Object.values(state.names)) {
      const holder = entry.holder ? state.sessions[entry.holder] : undefined;
      list.push({
        address: entry.name,
        name: entry.name,
        product: holder?.product ?? entry.product,
        status: holder ? "live" : "offline",
        unread: unreadFor(entry.name, entry.cursor),
        lastSeen: holder?.lastSeen ?? entry.lastSeen,
        you: entry.holder === session.key,
      });
    }
    for (const s of Object.values(state.sessions)) {
      if (s.name && state.names[s.name]?.holder === s.key) continue; // listed under its name
      const address = SESSION_PREFIX + s.key;
      list.push({ address, name: null, product: s.product, status: "live", unread: unreadFor(address, s.cursor), lastSeen: s.lastSeen, you: s.key === session.key });
    }
    list.sort(
      (a, b) =>
        Number(b.you) - Number(a.you) ||
        Number(b.status === "live") - Number(a.status === "live") ||
        a.address.localeCompare(b.address),
    );
    return { me: publicMe(me), peers: list };
  });
}
