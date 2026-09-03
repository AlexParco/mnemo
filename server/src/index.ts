/** mnemo MCP server — the storage layer of mnemo, usable from any MCP client.
 *
 * What this server deliberately does NOT do: decide what is worth remembering.
 * It cannot see the host conversation, so distilling a session into atomic
 * memories stays on the agent side. This process only offers the primitives and
 * keeps the store honest.
 *
 * This module only builds the server. Starting it lives in `bin.ts`, so nothing
 * here has a side effect on import and there is no "was I run directly?" test to
 * get wrong — see the comment there. */

import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { registerReadTools } from "./tools/read.js";
import { SERVER_INSTRUCTIONS } from "./prompts/criterion.js";
import { registerPrompts } from "./prompts/register.js";
import { registerDestructiveTools } from "./tools/destructive.js";
import { registerGuideTool } from "./tools/guide.js";
import { registerSyncTools } from "./tools/sync.js";
import { registerWriteTools } from "./tools/write.js";

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
