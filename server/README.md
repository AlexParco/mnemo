# mnemo-mcp

The mnemo store as an MCP server, so any MCP-capable coding agent can read the
same memory the Claude Code plugin writes. Same store, same format, same
environment variables — see [`docs/mcp-plan.md`](../docs/mcp-plan.md) for the
design and the roadmap.

**Status: P0–P5.** The storage layer and the behaviour layer. What is left is
rewiring the Claude Code plugin onto this (P6) and distribution (P7).

Per-client setup and what each one actually supports:
[`docs/mcp-clients.md`](../docs/mcp-clients.md).

## Run it

```bash
npm install && npm run build
MNEMO_DIR=/path/to/store node dist/src/bin.js   # speaks MCP over stdio
```

Wire it into a client (the shape is the same everywhere; the file differs):

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
| `mnemo_sync` | pull from the hub; reports conflicts instead of guessing |
| `mnemo_resolve_conflict` | write a merged file and stage it |
| `mnemo_rebase` | continue or abort the rebase a sync started |
| `mnemo_push` | publish, behind a secret scan that cannot be skipped |
| `mnemo_rename` | change a project's slug across the store |
| `mnemo_forget` | delete a project or a memory, overlap-safe |
| `mnemo_guide` | the usage criterion, to paste into a rules file |

## Prompts

`save_context`, `load_context`, `mem` and `sync_memory` carry the flows and the
judgement the tools cannot enforce. Claude Code, Cursor and VS Code surface them;
Codex does not implement prompts, and reads the server's `instructions` instead.
Both, plus `mnemo_guide`, are generated from `src/prompts/criterion.ts` — one
source, three channels, with a test that stops them drifting apart.

Writes are not committed as they happen: write what the session produced, then call
`mnemo_commit` once, so one session is one commit. Pushing is separate again — until
it runs, the memory is only on this machine.

### Two-phase operations

`mnemo_push`, `mnemo_rename` and `mnemo_forget` never act on the first call. They
report what would happen and hand back a value; passing it back is what acts. The
value is derived from the state they just described, so it stops working the moment
that state changes — a stale plan cannot be applied. Deleting a project deletes only
the memories tagged with it *alone*; anything shared with another project is untagged
and survives, and the report says which.

### The secret scan

Every push is scanned first, and there is no path to the network that skips it. On a
hit the push is refused and the findings come back with the matched text redacted —
`file:line` and `AKI…LE`, enough to open the note, not enough to use. A false positive
is unblocked by passing back the acknowledgement value the refusal issued, which is
derived from the commits and the findings and stops working the moment either changes.
Acknowledge only after the user has looked.

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
