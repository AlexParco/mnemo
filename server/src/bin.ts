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
import { WATCH_INTERVAL_MS, watch } from "./watch.js";

const USAGE = `mnemo-mcp ${VERSION}

  mnemo-mcp                      MCP over stdio (what a client launches locally)
  mnemo-mcp --http [--port N] [--bind ADDR]
                                 MCP over HTTP at /mcp, for agents on other machines.
                                 Needs MNEMO_TOKEN. Binds ${DEFAULT_BIND}:${DEFAULT_PORT} by default;
                                 reach it from another machine through an SSH tunnel.
  mnemo-mcp watch [--name N] [--url U] [--product P] [--server S] [--interval MS]
                                 Print one line per new mailbox message for a name, without
                                 marking anything read. Meant to run as a Claude Code monitor.
                                 Defaults: --name $MNEMO_AGENT, --url $MNEMO_URL (token from
                                 $MNEMO_TOKEN); without a URL it reads this machine's mailbox.
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

function flagValues(argv: string[], known: string[]): Record<string, string> {
  const values: Record<string, string> = {};
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]!;
    const eq = arg.indexOf("=");
    const flag = eq === -1 ? arg : arg.slice(0, eq);
    if (!known.includes(flag)) throw new HttpConfigError(`unknown argument: ${arg}\n\n${USAGE}`);
    const value = eq === -1 ? argv[++i] : arg.slice(eq + 1);
    if (value === undefined || value === "") throw new HttpConfigError(`${flag} needs a value.`);
    values[flag] = value;
  }
  return values;
}

async function runWatch(argv: string[]): Promise<void> {
  const flags = flagValues(argv, ["--name", "--url", "--product", "--server", "--interval"]);
  const name = (flags["--name"] ?? process.env.MNEMO_AGENT ?? "").trim();
  if (!name) {
    // A monitor in a chat with no mailbox name has nothing to announce: leave quietly.
    process.stderr.write("mnemo watch: no name to watch; set MNEMO_AGENT or pass --name.\n");
    return;
  }
  const url = (flags["--url"] ?? process.env.MNEMO_URL ?? "").trim() || undefined;
  const token = process.env.MNEMO_TOKEN ?? "";
  if (url && !token) throw new HttpConfigError("watching a remote server needs MNEMO_TOKEN.");
  const interval = flags["--interval"] !== undefined ? Number(flags["--interval"]) : WATCH_INTERVAL_MS;
  if (!Number.isInteger(interval) || interval < 100) throw new HttpConfigError("--interval must be an integer of at least 100 ms.");

  const controller = new AbortController();
  process.once("SIGINT", () => controller.abort());
  process.once("SIGTERM", () => controller.abort());
  await watch({
    name,
    ...(url ? { url, token } : {}),
    ...(flags["--product"] ? { product: flags["--product"] } : {}),
    ...(flags["--server"] ? { server: flags["--server"] } : {}),
    intervalMs: interval,
    signal: controller.signal,
  });
}

async function main(): Promise<void> {
  const argv = process.argv.slice(2);
  if (argv.includes("--help") || argv.includes("-h")) {
    process.stdout.write(USAGE);
    return;
  }
  if (argv[0] === "watch") return runWatch(argv.slice(1));
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
