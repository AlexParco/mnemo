/** The mailbox surface (P8): messages between agents, across machines and vendors.
 *
 * Nothing interrupts an agent when a message arrives — MCP is client to server — so
 * the descriptions carry the one habit that makes it work: check the inbox when you
 * start and between tasks, or wait on it when you have handed work off.
 *
 * Reading the inbox advances a cursor, the way opening mail marks it read. That is
 * the server's own bookkeeping, not anything the user owns, so the reading tools
 * are annotated read-only — which also keeps clients that ask before every write
 * (Codex's `writes` approval mode) from prompting on each inbox check. */

import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { callerFor } from "../mailbox/identity.js";
import { inbox, peers, register, reply, send, wait, WAIT_DEFAULT_MS, WAIT_MAX_MS, type Me, type Message } from "../mailbox/store.js";
import { mailboxDir, storeDir } from "../store/paths.js";
import { projectExists } from "../store/project.js";
import { StoreError } from "../store/write.js";
import { guarded, ok } from "./result.js";

function identityLine(me: Me): string {
  const base = me.name
    ? `You are ${me.name} (${me.product}).`
    : `You are ${me.sessionAddress} (${me.product}), unnamed — that address lasts only while this chat is open; mailbox_register gives you a durable name.`;
  return me.conflict ? `${base}\n⚠ ${me.conflict}` : base;
}

function formatMessage(m: Message): string {
  const lines = [`[${m.id}] ${m.type} from ${m.from} (${m.fromProduct}) to ${m.to} · ${m.ts}`];
  if (m.replyTo) lines.push(`in reply to ${m.replyTo}`);
  if (m.project) lines.push(`project: ${m.project} — load its memory with mnemo_load_project before acting on this`);
  lines.push("", m.body);
  if (m.type === "task") lines.push("", `When it is finished: mailbox_reply with id ${m.id} and type done.`);
  if (m.type === "question") lines.push("", `To answer: mailbox_reply with id ${m.id} and type answer.`);
  return lines.join("\n");
}

function formatInbox(messages: Message[], me: Me, emptyText: string): string[] {
  if (messages.length === 0) return [emptyText, identityLine(me)];
  return [messages.map(formatMessage).join("\n\n---\n\n"), identityLine(me)];
}

export function registerMailboxTools(server: McpServer): void {
  server.registerTool(
    "mailbox_register",
    {
      title: "mailbox: claim a name",
      description:
        "Claim a durable name other agents can reach you by — e.g. 'laptop-front' or 'vps-backend'. Messages sent to " +
        "that name wait for you even while this chat is closed, and arrive when you next read the inbox. Names are " +
        "lower-case letters, digits and dashes. A name another open chat already holds is refused. You may already " +
        "have one from configuration (MNEMO_AGENT); mailbox_peers shows who you are.",
      inputSchema: { name: z.string().describe("The name to claim, e.g. 'laptop-front'.") },
      annotations: { readOnlyHint: false, idempotentHint: true },
    },
    async ({ name }, extra) =>
      guarded(async () => {
        const me = await register(mailboxDir(), callerFor(server, extra), name);
        return ok(`${identityLine(me)} Messages sent to '${me.name}' reach you, including any that were already waiting.`);
      }),
  );

  server.registerTool(
    "mailbox_send",
    {
      title: "mailbox: send a message to another agent",
      description:
        "Leave a message for another agent — on this machine or another, in any MCP client (Claude Code, Codex, " +
        "opencode, Cursor). `to` is a name (the message waits even if that agent is offline), `@all`, `@<product>` " +
        "such as `@codex`, or a `session:` address from mailbox_peers (reaches only a chat that is open now). Types: " +
        "`task` asks for work, `question` asks for an answer, `note` just informs. Pass `project` to point the " +
        "receiver at that project's memory instead of pasting context into the body. To close a task or answer a " +
        "question, use mailbox_reply, not this.",
      inputSchema: {
        to: z.string().describe("A name, @all, @<product>, or a session: address."),
        type: z.enum(["task", "question", "note"]),
        body: z.string().describe("The message. Self-contained: the receiver did not see your conversation."),
        project: z.string().optional().describe("Project slug from the memory store the receiver should load first."),
      },
      annotations: { readOnlyHint: false, destructiveHint: false },
    },
    async ({ to, type, body, project }, extra) =>
      guarded(async () => {
        if (project !== undefined && !projectExists(storeDir(), project.trim())) {
          throw new StoreError(
            `No project '${project}' in the memory store, so the pointer would lead nowhere. Check the slug with mnemo_list_projects.`,
          );
        }
        const result = await send(mailboxDir(), callerFor(server, extra), { to, type, body, ...(project !== undefined ? { project } : {}) });
        const lines = [`Sent ${result.message.id} (${result.message.type}) to ${result.message.to}.`];
        if (result.warning) lines.push(`⚠ ${result.warning}`);
        if (type !== "note") lines.push(`The reply will arrive in your inbox; mailbox_wait blocks until it does.`);
        return ok(lines.join("\n"), identityLine(result.me));
      }),
  );

  server.registerTool(
    "mailbox_reply",
    {
      title: "mailbox: close a task or answer a question",
      description:
        "Reply to a message you received: `done` closes a `task`, `answer` answers a `question`. It must carry the id " +
        "of that exact message, and it goes back to whoever sent it. Only a recipient of the original can reply, and " +
        "only once per type — a completion that names the wrong message, or repeats itself, is refused.",
      inputSchema: {
        id: z.string().describe("The id of the message being closed, e.g. 'm1a3f0c'."),
        type: z.enum(["done", "answer"]),
        body: z.string().describe("What was done, or the answer — including anything the sender needs to act on."),
      },
      annotations: { readOnlyHint: false, destructiveHint: false },
    },
    async ({ id, type, body }, extra) =>
      guarded(async () => {
        const result = await reply(mailboxDir(), callerFor(server, extra), { id, type, body });
        const lines = [`Sent ${result.message.id} (${type} for ${id}) to ${result.message.to}.`];
        if (result.warning) lines.push(`⚠ ${result.warning}`);
        return ok(lines.join("\n"), identityLine(result.me));
      }),
  );

  server.registerTool(
    "mailbox_inbox",
    {
      title: "mailbox: read your messages",
      description:
        "Read messages other agents left for you — tasks, questions, answers, notes — and mark them read. Nothing " +
        "interrupts you when a message arrives, so check this when you start working and between tasks. A `task` " +
        "that names a project: load that project's memory before acting. Close tasks and answer questions with " +
        "mailbox_reply.",
      inputSchema: {},
      annotations: { readOnlyHint: true },
    },
    async (_args, extra) =>
      guarded(async () => {
        const result = await inbox(mailboxDir(), callerFor(server, extra));
        return ok(...formatInbox(result.messages, result.me, "No new messages."));
      }),
  );

  server.registerTool(
    "mailbox_wait",
    {
      title: "mailbox: wait for a message",
      description:
        `Block until a message arrives for you, or the timeout passes (default ${WAIT_DEFAULT_MS / 1000} s, at most ` +
        `${WAIT_MAX_MS / 1000} s — some clients cut tool calls at 60 s). Use it after handing work off, to wait for the ` +
        "`done` or `answer`. When it times out empty, call it again to keep waiting.",
      inputSchema: {
        timeout_ms: z.number().int().min(0).max(WAIT_MAX_MS).optional().describe("How long to wait, in milliseconds."),
      },
      annotations: { readOnlyHint: true },
    },
    async ({ timeout_ms }, extra) =>
      guarded(async () => {
        const result = await wait(mailboxDir(), callerFor(server, extra), timeout_ms ?? WAIT_DEFAULT_MS, extra.signal);
        return ok(
          ...formatInbox(result.messages, result.me, "Nothing arrived before the timeout. Call mailbox_wait again to keep waiting."),
        );
      }),
  );

  server.registerTool(
    "mailbox_peers",
    {
      title: "mailbox: who you can message",
      description:
        "List the agents you can reach: their names or session addresses, the client each runs in, whether it is " +
        "live right now, and how many messages are waiting for it. Use it to find the exact address before " +
        "mailbox_send, and to see who you are.",
      inputSchema: {},
      annotations: { readOnlyHint: true },
    },
    async (_args, extra) =>
      guarded(async () => {
        const result = await peers(mailboxDir(), callerFor(server, extra));
        if (result.peers.length === 0) return ok("No agents known yet.", identityLine(result.me));
        const rows = result.peers.map(
          (p) => `| ${p.address}${p.you ? " (you)" : ""} | ${p.product} | ${p.status} | ${p.unread} | ${p.lastSeen} |`,
        );
        const table = ["| address | client | status | unread | last seen |", "|---|---|---|---|---|", ...rows].join("\n");
        return ok(
          table,
          "live: its process was confirmed running just now. offline: a known name nobody holds — messages to it wait.",
          identityLine(result.me),
        );
      }),
  );
}
