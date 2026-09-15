/** Who is calling a mailbox tool, built from the MCP request.
 *
 * Three layers, first match wins: a configured name (the `X-Mnemo-Agent` header
 * over HTTP, `MNEMO_AGENT` from the client's `env` block over stdio), then a name the
 * chat registered, then its session address — which always exists, so a chat that
 * never did anything is still reachable. */

import { createHash, randomBytes } from "node:crypto";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { Env } from "../store/paths.js";
import type { Caller } from "./store.js";

/** Over stdio there is no transport session (`sessionId` is undefined), but every
 * stdio client spawns its own server process, so the process is the session. */
const PROCESS_KEY = `${process.pid.toString(36)}${randomBytes(2).toString("hex")}`;

/** Over HTTP the transport's session id is a UUID. Its digest is a shorter address,
 * stable for the life of the session and unique enough to tell chats apart. The HTTP
 * server uses the same function to release the session when it closes. */
export function httpSessionKey(sessionId: string): string {
  return createHash("sha256").update(sessionId).digest("hex").slice(0, 12);
}

export interface RequestContext {
  sessionId?: string;
  requestInfo?: { headers?: Record<string, string | string[] | undefined> };
}

/** The client product from the MCP handshake (`clientInfo.name`), normalised for
 * use in `@<product>` group addresses. */
export function productOf(server: McpServer): string {
  const raw = server.server.getClientVersion()?.name ?? "";
  return raw.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "") || "unknown";
}

export function callerFor(server: McpServer, request: RequestContext, env: Env = process.env): Caller {
  const overHttp = request.sessionId !== undefined;
  const header = request.requestInfo?.headers?.["x-mnemo-agent"];
  const configured = overHttp ? (Array.isArray(header) ? header[0] : header) : env.MNEMO_AGENT;
  const name = configured?.trim();
  return {
    sessionKey: overHttp ? httpSessionKey(request.sessionId!) : PROCESS_KEY,
    product: productOf(server),
    transport: overHttp ? "http" : "stdio",
    ...(name ? { configuredName: name } : {}),
  };
}
