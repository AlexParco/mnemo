#!/usr/bin/env node
/** The executable. Speaks MCP over stdio.
 *
 * It starts unconditionally, which is the whole point. The previous version
 * guarded startup with `import.meta.url === \`file://${process.argv[1]}\`` so
 * that importing the module from tests would not launch a server. That guard is
 * false whenever the file is reached through a symlink — which is exactly how
 * npm runs a `bin` — so the published package would have installed cleanly and
 * then done nothing at all. It also broke on paths containing spaces.
 *
 * Splitting the module in two removes the question instead of answering it:
 * `index.ts` builds a server and has no side effects, this file always runs one. */

import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { createServer, VERSION } from "./index.js";
import { storeDir } from "./store/paths.js";

async function main(): Promise<void> {
  const server = createServer();
  await server.connect(new StdioServerTransport());
  // stdout is the transport — anything human-readable goes to stderr.
  process.stderr.write(`mnemo-mcp ${VERSION} ready · store: ${storeDir()}\n`);
}

main().catch((err: unknown) => {
  process.stderr.write(`mnemo-mcp failed to start: ${err instanceof Error ? err.message : String(err)}\n`);
  process.exit(1);
});
