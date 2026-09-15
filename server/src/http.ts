/** The HTTP transport: what lets an agent on another machine reach this server.
 *
 * MCP has two transports, stdio and Streamable HTTP, and a remote server can only be
 * the second. Exposure is the whole risk, so the defaults are the conservative ones:
 * it binds 127.0.0.1 unless told otherwise, and a bearer token is required on every
 * request, loopback included. The intended way in from another machine is an SSH
 * tunnel (`ssh -L`), which keeps the port off the network entirely.
 *
 * The token also covers DNS rebinding: a page in a browser that tricks it into
 * talking to 127.0.0.1 still does not have the token.
 *
 * Each MCP session gets its own server instance and transport. They share the store
 * and the mailbox through the files and the lock, exactly as separate stdio processes
 * do — which is also why a stdio chat and an HTTP chat can message each other. */

import http from "node:http";
import { createHash, randomUUID, timingSafeEqual } from "node:crypto";
import type { AddressInfo } from "node:net";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { isInitializeRequest } from "@modelcontextprotocol/sdk/types.js";
import { createServer } from "./index.js";
import { httpSessionKey } from "./mailbox/identity.js";
import { HTTP_IDLE_MS, releaseSession } from "./mailbox/store.js";
import { mailboxDir } from "./store/paths.js";

export const DEFAULT_PORT = 7433;
export const DEFAULT_BIND = "127.0.0.1";
const MAX_BODY_BYTES = 4 * 1024 * 1024;
const SWEEP_MS = 60_000;

export interface HttpOptions {
  token: string;
  port?: number;
  bind?: string;
}

export interface RunningHttpServer {
  url: string;
  close(): Promise<void>;
}

export class HttpConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "HttpConfigError";
  }
}

export function isLoopback(bind: string): boolean {
  return bind === "localhost" || bind === "::1" || bind.startsWith("127.");
}

const digest = (value: string) => createHash("sha256").update(value).digest();

/** Compares digests, so neither the token's length nor its content leaks through timing. */
function authorized(header: string | undefined, token: string): boolean {
  if (!header?.startsWith("Bearer ")) return false;
  return timingSafeEqual(digest(header.slice("Bearer ".length).trim()), digest(token));
}

function sendJson(res: http.ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}): void {
  res.writeHead(status, { "content-type": "application/json", ...headers });
  res.end(JSON.stringify(body));
}

function rpcError(res: http.ServerResponse, status: number, message: string): void {
  sendJson(res, status, { jsonrpc: "2.0", error: { code: -32000, message }, id: null });
}

class BodyError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

async function readJsonBody(req: http.IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    const buf = chunk as Buffer;
    size += buf.length;
    if (size > MAX_BODY_BYTES) throw new BodyError(413, "request body too large");
    chunks.push(buf);
  }
  try {
    return JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch {
    throw new BodyError(400, "request body is not valid JSON");
  }
}

interface LiveSession {
  transport: StreamableHTTPServerTransport;
  lastActivity: number;
}

export async function startHttpServer(options: HttpOptions): Promise<RunningHttpServer> {
  const token = options.token.trim();
  if (!token) {
    throw new HttpConfigError("HTTP mode needs a token: set MNEMO_TOKEN. It is required even on 127.0.0.1.");
  }
  const bind = options.bind ?? DEFAULT_BIND;
  const port = options.port ?? DEFAULT_PORT;
  const sessions = new Map<string, LiveSession>();

  const release = (sessionId: string): void => {
    if (!sessions.delete(sessionId)) return;
    releaseSession(mailboxDir(), httpSessionKey(sessionId)).catch(() => {
      // Best effort: an unreleased session still expires after HTTP_IDLE_MS.
    });
  };

  async function handle(req: http.IncomingMessage, res: http.ServerResponse): Promise<void> {
    const url = new URL(req.url ?? "/", "http://mnemo.invalid");
    if (url.pathname !== "/mcp") return sendJson(res, 404, { error: "not found" });
    // Before anything else, including reading the body: an unauthenticated request
    // must not be able to create a session or touch the store.
    if (!authorized(req.headers.authorization, token)) {
      return sendJson(res, 401, { error: "unauthorized" }, { "www-authenticate": 'Bearer realm="mnemo"' });
    }

    const header = req.headers["mcp-session-id"];
    const sessionId = Array.isArray(header) ? header[0] : header;

    if (req.method === "POST") {
      let body: unknown;
      try {
        body = await readJsonBody(req);
      } catch (err) {
        return rpcError(res, err instanceof BodyError ? err.status : 400, err instanceof Error ? err.message : "bad request");
      }

      if (sessionId) {
        const live = sessions.get(sessionId);
        if (!live) return rpcError(res, 404, "Session not found");
        live.lastActivity = Date.now();
        return live.transport.handleRequest(req, res, body);
      }

      if (!isInitializeRequest(body)) {
        return rpcError(res, 400, "Bad Request: no session. The first request must be initialize.");
      }
      const transport: StreamableHTTPServerTransport = new StreamableHTTPServerTransport({
        sessionIdGenerator: () => randomUUID(),
        onsessioninitialized: (id) => {
          sessions.set(id, { transport, lastActivity: Date.now() });
        },
        onsessionclosed: (id) => release(id),
      });
      // Set before connect: the SDK wraps whatever onclose is already there.
      transport.onclose = () => {
        if (transport.sessionId) release(transport.sessionId);
      };
      await createServer().connect(transport);
      return transport.handleRequest(req, res, body);
    }

    if (req.method === "GET" || req.method === "DELETE") {
      if (!sessionId) return rpcError(res, 400, "Bad Request: missing Mcp-Session-Id.");
      const live = sessions.get(sessionId);
      if (!live) return rpcError(res, 404, "Session not found");
      live.lastActivity = Date.now();
      return live.transport.handleRequest(req, res);
    }

    res.writeHead(405, { allow: "GET, POST, DELETE" });
    res.end();
  }

  const server = http.createServer((req, res) => {
    handle(req, res).catch((err: unknown) => {
      process.stderr.write(`mnemo-mcp http: ${err instanceof Error ? err.message : String(err)}\n`);
      if (!res.headersSent) rpcError(res, 500, "internal error");
      else res.end();
    });
  });

  // Close sessions nobody has used in a while. The store expires them on the same
  // clock, so the name and the transport go together.
  const sweep = setInterval(() => {
    const cutoff = Date.now() - HTTP_IDLE_MS;
    for (const live of sessions.values()) {
      if (live.lastActivity < cutoff) live.transport.close().catch(() => {});
    }
  }, SWEEP_MS);
  sweep.unref();

  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, bind, () => {
      server.off("error", reject);
      resolve();
    });
  });

  const address = server.address() as AddressInfo;
  const host = address.family === "IPv6" ? `[${address.address}]` : address.address;
  return {
    url: `http://${host}:${address.port}/mcp`,
    async close() {
      clearInterval(sweep);
      for (const live of [...sessions.values()]) await live.transport.close().catch(() => {});
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}
