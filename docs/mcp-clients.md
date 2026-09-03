# Which clients get how much of mnemo

The plan flagged one thing it could not answer from the repo: how much MCP prompt
support exists per client, because that decides how much of mnemo's *criterion* —
as opposed to its storage — survives outside Claude Code.

**Checked 2026-09-01, against each client's own documentation.** This is external
and moves; re-check before relying on it.

| Client | Tools | Prompts | Resources | Elicitation | `instructions` |
|---|:--:|:--:|:--:|:--:|:--:|
| [Claude Code](https://code.claude.com/docs/en/mcp) | ✅ | ✅ | ✅ | — | — |
| [Cursor](https://cursor.com/docs/context/mcp) | ✅ | ✅ | ✅ | ✅ | — |
| [VS Code / Copilot](https://code.visualstudio.com/docs/copilot/chat/mcp-servers) | ✅ | ✅ | ✅ | — | — |
| [Codex](https://learn.chatgpt.com/docs/extend/mcp) | ✅ | ❌ | ❌ | ❌ | ✅ |

Read the blanks as "not documented on the page checked", not as "absent". The two
findings that actually changed the design are the ones that are stated outright.

## What this changed

**Codex documents neither prompts nor resources — but it does read the server's
`instructions` field** and "uses it as guidance alongside tools". That makes
`instructions` the broadest channel mnemo has, not a courtesy header. It now
carries the core of the criterion: load before working, search before answering
from memory, one fact per note, don't save what the code already says, and the
fact that the server cannot see the conversation so the judgement is the agent's.
It is deliberately short, because it is paid for on every connection.

**Codex's `default_tools_approval_mode = "writes"` prompts for tools that are not
marked read-only.** So the `readOnlyHint` annotations on mnemo's five read tools
are not decoration: they are what lets a user run in `writes` mode and be asked
about writes and pushes while loads and searches go through unattended. The test
suite asserts every tool carries the right hint for that reason.

## The three channels

| Channel | Reaches | Carries |
|---|---|---|
| Server `instructions` | every client; the only one documented for Codex | the core criterion, short |
| MCP prompts | Claude Code, Cursor, VS Code | the full flows: `save_context`, `load_context`, `mem`, `sync_memory` |
| `mnemo_guide` → rules file | every client, but the user has to paste it | the whole criterion as markdown |

All three are generated from one module, `server/src/prompts/criterion.ts`, and a
test asserts the rules a tool returns are the same objects the guide contains —
so a host reading one channel cannot get a stale version of a rule another channel
states differently.

## Configuring it

Build first: `cd server && npm install && npm run build`.

**Claude Code** — `.mcp.json` in the project, or `claude mcp add`:

```json
{
  "mcpServers": {
    "mnemo": {
      "command": "node",
      "args": ["/absolute/path/to/mnemo/server/dist/src/bin.js"],
      "env": { "MNEMO_MACHINE": "laptop" }
    }
  }
}
```

**Cursor** — `mcp.json`, same shape as above (`mcpServers`, `command`, `args`, `env`).

**Codex** — `~/.codex/config.toml`, or `.codex/config.toml` for a trusted project:

```toml
[mcp_servers.mnemo]
command = "node"
args = ["/absolute/path/to/mnemo/server/dist/src/bin.js"]

[mcp_servers.mnemo.env]
MNEMO_MACHINE = "laptop"
```

**VS Code / Copilot** — supported, and it surfaces MCP prompts as `/<server>.<prompt>`
slash commands. Its config file format was not verified here; follow the linked docs.

For any client that does not surface prompts, also run `mnemo_guide` once and paste
its output into that project's rules file.

Environment in every case: `MNEMO_DIR`, `MNEMO_REMOTE`, `MNEMO_MACHINE`, `MNEMO_LANG`
— the same contract the plugin's skills use.
