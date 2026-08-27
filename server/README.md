# mnemo-mcp

The mnemo store as an MCP server, so any MCP-capable coding agent can read the
same memory the Claude Code plugin writes. Same store, same format, same
environment variables — see [`docs/mcp-plan.md`](../docs/mcp-plan.md) for the
design and the roadmap.

**Status: P0 + P1 + P2.** Reads, writes and local commits. Git sync/push and the
destructive operations (rename, forget) are still plugin-only (P3–P4).

## Run it

```bash
npm install && npm run build
MNEMO_DIR=/path/to/store node dist/src/index.js   # speaks MCP over stdio
```

Wire it into a client (the shape is the same everywhere; the file differs):

```json
{
  "mcpServers": {
    "mnemo": {
      "command": "node",
      "args": ["/absolute/path/to/mnemo/server/dist/src/index.js"],
      "env": { "MNEMO_MACHINE": "laptop" }
    }
  }
}
```

Environment: `MNEMO_DIR`, `MNEMO_REMOTE`, `MNEMO_MACHINE`, `MNEMO_LANG` — the
same contract the skills use.

## Tools

| Tool | What it does |
|---|---|
| `mnemo_status` | store path, remote, unpushed commits, this machine's label |
| `mnemo_list_projects` | the overview table: slug, status, memories, services |
| `mnemo_load_project` | resume card + full detail + the machine rule |
| `mnemo_search_memories` | term search across ids, bodies, tags, services |
| `mnemo_read_memory` | one memory, in full |
| `mnemo_bootstrap` | create the store, or adopt one from the hub |
| `mnemo_upsert_project` | create or update a project |
| `mnemo_write_memory` | save one atomic fact |
| `mnemo_write_pending` | replace a project's pending list |
| `mnemo_commit` | commit the batch, locally |

Writes are not committed as they happen: write what the session produced, then call
`mnemo_commit` once, so one session is one commit. Pushing is a separate step and
does not exist yet — until it does, a commit stays on this machine.

## Tests

```bash
npm test
```

The suite's centre of gravity is `test/card-parity.test.ts`: it runs the original
`skills/load-context/card.py` and asserts the TypeScript render matches it **byte
for byte**, for every fixture project in both languages and on both sides of the
`[@machine]` flag. It needs `python3` and skips itself without one.

To confirm the gate still bites, break it on purpose — e.g. make `truncate` in
`src/store/text.ts` slice by `String.length` instead of code points. Four parity
cases must fail.
