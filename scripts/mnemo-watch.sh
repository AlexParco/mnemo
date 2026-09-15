#!/bin/sh
# Claude Code monitor: announce mailbox messages for this chat.
#
# Every line this prints reaches Claude as a notification, and a notification wakes a
# chat that is idle at the prompt, so nobody has to ask it to check its inbox. It only
# announces; the chat still reads with mailbox_inbox.
#
#   MNEMO_AGENT          the name to watch; without it there is nothing to do
#   MNEMO_URL            a remote server's MCP URL; without it, this machine's mailbox
#   MNEMO_TOKEN          the server's token, when MNEMO_URL is set
#   MNEMO_WATCH_SERVER   the MCP server name the chat uses for the mailbox, e.g. mnemo-vps
[ -n "${MNEMO_AGENT:-}" ] || exit 0

ROOT="${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
for BIN in "$ROOT/server/dist/src/bin.js" "${CLAUDE_PLUGIN_DATA:-/nonexistent}/server/dist/src/bin.js"; do
  if [ -f "$BIN" ]; then
    if [ -n "${MNEMO_WATCH_SERVER:-}" ]; then
      exec node "$BIN" watch --product claude-code --server "$MNEMO_WATCH_SERVER"
    fi
    exec node "$BIN" watch --product claude-code
  fi
done

echo "mnemo watch: the server is not built yet; it builds on the next session start." >&2
exit 0
