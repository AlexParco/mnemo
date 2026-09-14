#!/bin/sh
# Start the mnemo MCP server for the Claude Code plugin.
#
# The plugin ships from git with no build step, so where the server lives depends
# on how it was obtained. In order:
#   1. MNEMO_MCP_COMMAND — an explicit override, for development or a custom build.
#   2. A built checkout inside the plugin (server/dist), which is what you get
#      after `cd server && npm install && npm run build`. First, because someone
#      who built it there did so on purpose.
#   3. The build the SessionStart hook keeps in the plugin's persistent data
#      directory — the normal case for an installed plugin.
#   4. The published package, once it is on npm.
# Anything else is a setup problem, and says so rather than failing silently:
# a dead MCP server would leave the skills calling tools that are not there.
set -e

if [ -n "${MNEMO_MCP_COMMAND:-}" ]; then
  exec sh -c "$MNEMO_MCP_COMMAND"
fi

ROOT="${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
LOCAL="$ROOT/server/dist/src/bin.js"
if [ -f "$LOCAL" ]; then
  exec node "$LOCAL"
fi

DATA_BIN="${CLAUDE_PLUGIN_DATA:-}/server/dist/src/bin.js"
if [ -n "${CLAUDE_PLUGIN_DATA:-}" ] && [ -f "$DATA_BIN" ]; then
  exec node "$DATA_BIN"
fi

if command -v npx >/dev/null 2>&1; then
  exec npx -y @alexparco/mnemo-mcp
fi

cat >&2 <<'MSG'
mnemo: could not start the MCP server.

Tried, in order:
  $MNEMO_MCP_COMMAND         (not set)
  <plugin>/server/dist       (not built)
  <plugin-data>/server/dist  (the SessionStart hook builds this; it may still be running)
  npx @alexparco/mnemo-mcp   (npx not found)

Until one of those resolves, the /mnemo:* commands have no tools to call. If you
just installed the plugin, the build runs on the next session start — wait for it
to report `server ready`, then run /reload-plugins.
MSG
exit 1
