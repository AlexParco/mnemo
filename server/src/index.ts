#!/usr/bin/env node
/** mnemo MCP server — the storage layer of mnemo, usable from any MCP client.
 *
 * What this server deliberately does NOT do: decide what is worth remembering.
 * It cannot see the host conversation, so distilling a session into atomic
 * memories stays on the agent side. This process only offers the primitives and
 * keeps the store honest. */

import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { registerReadTools } from "./tools/read.js";
import { SERVER_INSTRUCTIONS } from "./prompts/criterion.js";
import { registerPrompts } from "./prompts/register.js";
import { registerDestructiveTools } from "./tools/destructive.js";
import { registerGuideTool } from "./tools/guide.js";
import { registerSyncTools } from "./tools/sync.js";
import { registerWriteTools } from "./tools/write.js";
import { storeDir } from "./store/paths.js";

export const VERSION = "0.1.0";

export function createServer(): McpServer {
  const server = new McpServer(
    { name: "mnemo", version: VERSION },
    {
      instructions: SERVER_INSTRUCTIONS,
    },
  );
  registerReadTools(server);
  registerWriteTools(server);
  registerSyncTools(server);
  registerDestructiveTools(server);
  registerGuideTool(server);
  registerPrompts(server);
  return server;
}

async function main(): Promise<void> {
  const server = createServer();
  await server.connect(new StdioServerTransport());
  // stdout is the transport — anything human-readable goes to stderr.
  process.stderr.write(`mnemo-mcp ${VERSION} ready · store: ${storeDir()}\n`);
}

const invokedDirectly = process.argv[1] !== undefined && import.meta.url === `file://${process.argv[1]}`;
if (invokedDirectly) {
  main().catch((err: unknown) => {
    process.stderr.write(`mnemo-mcp failed to start: ${err instanceof Error ? err.message : String(err)}\n`);
    process.exit(1);
  });
}
