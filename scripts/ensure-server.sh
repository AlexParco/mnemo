#!/bin/sh
# SessionStart hook: make sure the bundled MCP server is built, once per version.
#
# A plugin installed from git arrives as source — there is no build step — so
# without this the /mnemo:* commands would come up with no tools behind them and
# the user would have to find the plugin directory and run npm by hand.
#
# It builds into ${CLAUDE_PLUGIN_DATA}, not into the plugin: that directory is
# meant to survive plugin updates, so an update does not leave a stale dist/ and
# does not force a rebuild when nothing changed.
#
# Two rules: the fast path costs nothing (it runs on every session start), and
# nothing here ever fails the session — a memory plugin that cannot build is a
# nuisance, a hook that breaks Claude Code is not.
set -u

ROOT="${CLAUDE_PLUGIN_ROOT:-}"
DATA="${CLAUDE_PLUGIN_DATA:-}"
[ -n "$ROOT" ] && [ -n "$DATA" ] || exit 0
command -v node >/dev/null 2>&1 || exit 0
command -v npm >/dev/null 2>&1 || exit 0

VERSION=$(node -p "require('$ROOT/.claude-plugin/plugin.json').version" 2>/dev/null) || exit 0
[ -n "$VERSION" ] || exit 0

STAMP="$DATA/.built"
BIN="$DATA/server/dist/src/bin.js"

# Fast path.
if [ -f "$BIN" ] && [ "$(cat "$STAMP" 2>/dev/null)" = "$VERSION" ]; then
  exit 0
fi

# One builder at a time. mkdir is atomic, so a second session starting mid-build
# steps aside instead of racing it.
mkdir -p "$DATA" 2>/dev/null || exit 0
LOCK="$DATA/.build-lock"
mkdir "$LOCK" 2>/dev/null || exit 0
trap 'rmdir "$LOCK" 2>/dev/null' EXIT INT TERM

echo "mnemo: building its MCP server for v$VERSION. Once per version."

# The layout mirrors the repo's, because the build and the runtime both resolve
# templates/SCHEMA.md relative to the server directory.
rm -rf "$DATA/server" "$DATA/templates"
mkdir -p "$DATA/server"
cp -R "$ROOT/templates" "$DATA/templates" 2>/dev/null || true
for item in package.json package-lock.json tsconfig.json src scripts; do
  cp -R "$ROOT/server/$item" "$DATA/server/$item" 2>/dev/null || true
done

if (cd "$DATA/server" && npm ci --no-audit --no-fund >/dev/null 2>&1 && npm run build >/dev/null 2>&1); then
  printf '%s' "$VERSION" > "$STAMP"
  echo "mnemo: server ready. If the mnemo_* tools are missing this session, run /reload-plugins."
else
  rm -f "$STAMP"
  echo "mnemo: could not build its MCP server. To do it by hand: cd \"$DATA/server\" && npm ci && npm run build"
fi
exit 0
