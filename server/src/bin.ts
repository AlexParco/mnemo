#!/usr/bin/env node
/** The executable. Speaks MCP over stdio by default, or over HTTP with `--http`.
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
import { DEFAULT_BIND, DEFAULT_PORT, HttpConfigError, isLoopback, startHttpServer } from "./http.js";
import { mailboxDir, storeDir } from "./store/paths.js";

const USAGE = `mnemo-mcp ${VERSION}

  mnemo-mcp                      MCP over stdio (what a client launches locally)
  mnemo-mcp --http [--port N] [--bind ADDR]
                                 MCP over HTTP at /mcp, for agents on other machines.
                                 Needs MNEMO_TOKEN. Binds ${DEFAULT_BIND}:${DEFAULT_PORT} by default;
                                 reach it from another machine through an SSH tunnel.
`;

interface Args {
  http: boolean;
  port: number;
  bind: string;
}

export function parseArgs(argv: string[]): Args {
  const args: Args = { http: false, port: DEFAULT_PORT, bind: DEFAULT_BIND };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]!;
    const [flag, inline] = arg.includes("=") ? [arg.slice(0, arg.indexOf("=")), arg.slice(arg.indexOf("=") + 1)] : [arg, undefined];
    const value = () => {
      const v = inline ?? argv[++i];
      if (v === undefined || v === "") throw new HttpConfigError(`${flag} needs a value.`);
      return v;
    };
    if (flag === "--http") args.http = true;
    else if (flag === "--port") {
      const port = Number(value());
      if (!Number.isInteger(port) || port < 0 || port > 65535) throw new HttpConfigError(`--port must be an integer from 0 to 65535.`);
      args.port = port;
    } else if (flag === "--bind") args.bind = value();
    else throw new HttpConfigError(`unknown argument: ${arg}\n\n${USAGE}`);
  }
  return args;
}

async function main(): Promise<void> {
  const argv = process.argv.slice(2);
  if (argv.includes("--help") || argv.includes("-h")) {
    process.stdout.write(USAGE);
    return;
  }
  const args = parseArgs(argv);

  if (!args.http) {
    const server = createServer();
    await server.connect(new StdioServerTransport());
    // stdout is the transport — anything human-readable goes to stderr.
    process.stderr.write(`mnemo-mcp ${VERSION} ready · store: ${storeDir()}\n`);
    return;
  }

  const running = await startHttpServer({ token: process.env.MNEMO_TOKEN ?? "", port: args.port, bind: args.bind });
  if (!isLoopback(args.bind)) {
    process.stderr.write(
      `⚠ bound to ${args.bind}: reachable from the network, with the token as the only thing in the way. ` +
        `Prefer ${DEFAULT_BIND} and an SSH tunnel.\n`,
    );
  }
  process.stderr.write(`mnemo-mcp ${VERSION} listening on ${running.url} · store: ${storeDir()} · mailbox: ${mailboxDir()}\n`);

  const stop = () => {
    running.close().finally(() => process.exit(0));
  };
  process.once("SIGINT", stop);
  process.once("SIGTERM", stop);
}

main().catch((err: unknown) => {
  process.stderr.write(`mnemo-mcp failed to start: ${err instanceof Error ? err.message : String(err)}\n`);
  process.exit(1);
});
