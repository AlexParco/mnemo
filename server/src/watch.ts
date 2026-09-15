/** `mnemo-mcp watch`: announce new mailbox messages for one name, one line each.
 *
 * Built to run as a Claude Code monitor. Every stdout line becomes a notification,
 * and a notification wakes a chat that is idle at the prompt — so the agent no longer
 * has to be told to check its inbox.
 *
 * It only looks. It never marks a message read and never claims the name: the chat
 * holding the name still reads everything itself with mailbox_inbox. Each message is
 * announced once per watcher run.
 *
 * With a URL it asks a remote server's read-only `/mailbox/unread` endpoint; without
 * one it reads the mailbox on this machine. Errors go to stderr and it keeps going,
 * because a tunnel that drops for a minute should not end the watch. */

import { setTimeout as sleep } from "node:timers/promises";
import { peekUnread, type Message } from "./mailbox/store.js";
import { mailboxDir } from "./store/paths.js";

export const WATCH_INTERVAL_MS = 2_000;
const PREVIEW_CHARS = 120;

export interface WatchOptions {
  name: string;
  url?: string;
  token?: string;
  product?: string;
  /** The MCP server name the chat knows the mailbox by, e.g. `mnemo-vps`. */
  server?: string;
  intervalMs?: number;
  signal?: AbortSignal;
  out?: (line: string) => void;
  err?: (line: string) => void;
}

export function formatNotice(m: Message, server?: string): string {
  const body = m.body.replace(/\s+/g, " ").trim();
  const preview = body.length > PREVIEW_CHARS ? `${body.slice(0, PREVIEW_CHARS - 1)}…` : body;
  const reply = m.replyTo ? ` in reply to ${m.replyTo}` : "";
  const where = server ? ` on the ${server} server` : "";
  return `[mnemo] ${m.type} ${m.id} from ${m.from}${reply}: "${preview}" Read it with mailbox_inbox${where}.`;
}

async function fetchUnread(o: WatchOptions, after: number): Promise<{ lastSeq: number; messages: Message[] }> {
  const endpoint = new URL(`${o.url!.replace(/\/mcp\/?$/, "").replace(/\/+$/, "")}/mailbox/unread`);
  endpoint.searchParams.set("name", o.name);
  endpoint.searchParams.set("after", String(after));
  if (o.product) endpoint.searchParams.set("product", o.product);
  const res = await fetch(endpoint, { headers: { authorization: `Bearer ${o.token ?? ""}` } });
  if (!res.ok) throw new Error(`HTTP ${res.status} from ${endpoint.origin}`);
  return (await res.json()) as { lastSeq: number; messages: Message[] };
}

export async function watch(o: WatchOptions): Promise<void> {
  const out = o.out ?? ((line: string) => process.stdout.write(line + "\n"));
  const err = o.err ?? ((line: string) => process.stderr.write(line + "\n"));
  const interval = o.intervalMs ?? WATCH_INTERVAL_MS;
  let after = 0;
  let lastError = "";
  for (;;) {
    if (o.signal?.aborted) return;
    try {
      const result = o.url ? await fetchUnread(o, after) : peekUnread(mailboxDir(), o.name, after, o.product);
      for (const message of result.messages) out(formatNotice(message, o.server));
      after = Math.max(after, result.lastSeq);
      lastError = "";
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      // Report a failure once, not every two seconds for as long as it lasts.
      if (message !== lastError) err(`mnemo watch: ${message}`);
      lastError = message;
    }
    try {
      await sleep(interval, undefined, o.signal ? { signal: o.signal } : undefined);
    } catch {
      return; // aborted
    }
  }
}
