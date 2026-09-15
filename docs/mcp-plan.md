# mnemo as an MCP server — implementation plan

> Status: **P0–P6 landed** — storage, behaviour, and the Claude Code plugin rewired
> onto the server. 17 tools, 4 prompts, 227 tests green, merged to `main`.
> P7 (distribution) is proposal with its blocking bug fixed. **P8 (the mailbox) is the
> stated end goal: steps 1–2 landed (mailbox over stdio, 23 tools, 247 tests); HTTP next.**

## Goal

Make the mnemo store usable from **any MCP-capable coding agent** (Cursor, Codex CLI, Cline,
Windsurf, Zed, Copilot agent mode, Gemini CLI, Claude Code…) without changing the store format
and without losing what the Claude Code plugin already does well.

**Non-goals.** Not a rewrite: the store stays plain `.md` + git, `templates/SCHEMA.md` is frozen,
and a v0.2 store must keep working untouched. Not a replacement of the plugin either — see
Architecture.

## The constraint that shapes everything

An MCP server **cannot see the host conversation**. So `save-context` — "distill the session,
one fact per file, don't save what's already in the code" — is not portable as a tool. The server
can only offer the primitive (`write_memory`). The *judgment* stays on the model side and has to
be re-shipped per host.

This splits mnemo into two layers that ship separately:

| Layer | What it holds | Ships as |
|---|---|---|
| **Storage** | store bootstrap, frontmatter parse/serialize, project filtering, card render, git sync/commit/push, secret scan, overlap-safe rename/forget | **MCP server** (portable) |
| **Behavior** | what is worth persisting, overlap vs `services`, when to stamp `[@machine]`, semantic-conflict judgement | MCP prompts + per-host rules file + CC skills (per host) |

## Architecture: hybrid, not replacement

```
                    ┌──────────────────────────────┐
  Claude Code  ────► │                              │
  Cursor       ────► │   mnemo MCP server (node)    │ ──► ~/.local/share/mnemo
  Codex CLI    ────► │   storage + git + card       │     (unchanged format)
  …            ────► │                              │
                    └──────────────────────────────┘
  Claude Code additionally keeps: hooks/suggest-save.js  (no MCP equivalent exists)
```

The Claude Code plugin survives as a **thin layer**: it keeps the `PreToolUse` nudge hook, which
has no MCP equivalent, and its skills shrink to "call the mnemo tools + here is the distillation
criterion". One store, two frontends.

## Runtime decision

**Node + TypeScript, distributed via `npx`.** Reasons: the widest install base across MCP clients,
one runtime instead of the current two (node for the hook, python3 for the card), and it keeps
`scripts/suggest-save.js` in the same toolchain.

Consequence: **`skills/load-context/card.py` gets ported to TS**. Its output is a hard contract —
see the golden-file test in Phase 1. `server/src/card/cli.ts` is a drop-in replacement with the same
argv, stdout and exit codes, so the plugin can drop its python3 dependency in P6. The Python script
stays until then: it is the parity oracle.

## Repo layout after the change

```
mnemo/
  server/                    # NEW — the MCP server
    src/store/               #   path resolution, frontmatter, project index, lock
    src/git/                 #   sync, commit, push, secret scan, conflict surface
    src/card/                #   port of card.py
    src/tools/               #   one file per tool
    src/prompts/             #   the distillation criterion, as MCP prompts
    test/fixtures/store/     #   a fixture store for golden tests
  .claude-plugin/            # unchanged (Claude Code frontend)
  skills/                    # thinned: criterion only, mechanics delegated to tools
  hooks/ scripts/            # unchanged — Claude Code only
  templates/SCHEMA.md        # FROZEN — the contract both frontends share
  docs/mcp-plan.md           # this file
```

## Tool surface

Naming: `mnemo_*`. Every tool resolves the store from `MNEMO_DIR` (default `~/.local/share/mnemo`)
and honours `MNEMO_REMOTE`, `MNEMO_AUTOPUSH`, `MNEMO_MACHINE` exactly as the skills do today.

### Read

| Tool | Params | Returns |
|---|---|---|
| `mnemo_status` | — | store path, has-remote, machine label, unpushed commits, dirty worktree, rebase-in-progress |
| `mnemo_list_projects` | — | per project: `slug, name, status, services, updated, memories, shared`; plus totals |
| `mnemo_load_project` | `slug`, `lang?` | `{ card, index, pending, memories[] }` — `card` is the verbatim render, `memories[]` is the full detail |
| `mnemo_search_memories` | `query?`, `project?`, `type?`, `limit?` | matching memories (frontmatter + summary) |
| `mnemo_read_memory` | `id` | full note |

`mnemo_load_project` returning **both** the card and the raw detail mirrors `load-context` step 3+4:
the agent gets everything in context, but has a canonical thing to print.

### Write

| Tool | Params | Returns |
|---|---|---|
| `mnemo_write_memory` | `{ id?, projects[], services?, tags?, type, body }` | `{ id, created\|updated, duplicateCandidates[] }` |
| `mnemo_write_pending` | `slug`, `content` | ok — full-file write; sections stay free-form |
| `mnemo_upsert_project` | `slug`, `name`, `status?`, `services?` | creates `INDEX.md` + empty `pending.md` if absent |
| `mnemo_commit` | `message` | `{ sha }` — `add -A` + commit, never adds a `Co-Authored-By` trailer |

Writes leave the worktree dirty; the commit is explicit. That preserves today's shape, where one
`save-context` produces N memories + a pending update in **one** commit.

`duplicateCandidates` is how "before creating, search for a duplicate" (SCHEMA rule) stops being a
thing the model must remember to do.

### Git

| Tool | Params | Returns |
|---|---|---|
| `mnemo_sync` | — | `pull --rebase --autostash`; returns `{ ok, conflicts[], rebaseInProgress }` |
| `mnemo_resolve_conflict` | `file`, `content` | writes the resolution + `git add` |
| `mnemo_rebase_continue` | — | runs with `GIT_EDITOR=true` internally |
| `mnemo_push` | — | **runs the secret scan first, always**; on a hit returns `refused` + the matching lines and does not push |

Conflict *resolution* stays with the model (it is semantic), but the git mechanics do not. The
`GIT_EDITOR=true` workaround the skills repeat in three files disappears into the implementation.

### Destructive — two-phase

| Tool | Params | Returns |
|---|---|---|
| `mnemo_rename` | `from`, `to`, `confirm?` | without `confirm`: the impact + a confirmation value. With it: applies |
| `mnemo_forget` | `kind: project\|memory`, `target`, `confirm?` | without `confirm`: for a project, the **exclusive** (delete) vs **shared** (untag) split + a value. With it: applies |

Two tools rather than the four sketched here: the phase is the presence of
`confirm`, which is the shape `mnemo_push` already uses for its secret-scan
acknowledgement. One pattern for every operation that must not act unasked.

MCP *elicitation* exists in the spec but client support is uneven — **verify per client before
relying on it**. The plan/apply token pair is client-agnostic and makes the confirmation structural
instead of prompt-dependent.

Tokens are single-use and invalidated if the store HEAD moves between plan and apply.

**Tool count is 14** as built (5 read, 5 write, 4 sync) — `mnemo_rebase` takes a
`continue`/`abort` argument rather than being two tools. Some hosts degrade with large
tool lists; if that bites, the next consolidation is `mnemo_read_memory` into
`mnemo_search_memories`.

## Invariants that move from prose into code

These are today instructions a model can skip. After the port they are not optional:

1. **Secret scan before push** (`save-context` step 7a) — in `mnemo_push`, unconditional, including
   under `MNEMO_AUTOPUSH`.
2. **Overlap-safe delete** — a memory tagged with more than one project is untagged, never deleted.
3. **Real frontmatter parsing** instead of `grep -rl "projects:.*<slug>"`. Kills the false-positive
   the skills warn about twice (`mnemo` matching inside `mnemo-web`).
4. **`id` == filename**, kebab-case, uniqueness checked on write.
5. **Store lock.** New: two agents can now hit the same store concurrently. A lockfile around every
   write/git op, with a stale-lock timeout.
6. **No `Co-Authored-By`** in any commit the server makes.

## What stays on the model side, and how it gets there

The distillation criterion — `save-context` steps 3–5 and the `[@machine]` stamping test — ships
three ways, best-effort per host:

- **MCP prompts** (`save_context`, `load_context`, `mem`, …) for hosts that surface them.
- **A rules snippet** the user pastes into `AGENTS.md` / `.cursorrules` / `CLAUDE.md`, generated by
  a `mnemo_guide` tool so it is always in sync with the server version.
- **Claude Code skills**, which keep the full criterion and delegate mechanics to the tools.

Honest limitation to document in the README: on hosts with no prompt support and a rules file the
model treats loosely, mnemo degrades to "correct storage, weaker curation".

## Phases

Each phase is independently shippable and testable.

**P0 — store library. ✅ done.** Path resolution, frontmatter parse/serialize (round-trip safe), project
index, lockfile. Fixture store under `server/test/fixtures/store/`.
*Done when:* round-trip of every fixture note is byte-identical.

**P1 — read-only tools. ✅ done.** `status`, `list_projects`, `load_project`, `search_memories`,
`read_memory` + the card port.
*Done when:* the TS card output matches `card.py` **byte-for-byte** on the fixture store, for both
`en` and `es`, including the `⚠` machine flags. This is the acceptance gate for the port.
*Shippable:* agents everywhere can already read memory.

**P2 — write tools + bootstrap. ✅ done.** `write_memory`, `write_pending`, `upsert_project`, `commit`,
plus lazy store creation replicating `save-context`'s bootstrap block (including remote adoption
before writing local templates — the ordering there is load-bearing).
*Done when:* a fresh `MNEMO_DIR` bootstraps, and a second machine pointed at the same
`MNEMO_REMOTE` adopts the existing history instead of creating an empty store.

**P3 — git. ✅ done.** `sync`, `resolve_conflict`, `rebase_continue`, `push` + secret scan.
*Done when:* integration tests pass against a local bare repo acting as the hub, and the secret
corpus (private key, `AKIA…`, `ghp_…`, `xox…`, `user:pass@host`) is refused 100% of the time.

**P4 — destructive. ✅ done.** `rename_plan/apply`, `forget_plan/apply`.
*Done when:* deleting a project leaves every shared memory alive and correctly untagged, verified
by reading `projects:` fields, not by grepping the slug.

**P5 — behaviour layer. ✅ done.** MCP prompts + `mnemo_guide` + per-client config, in
[`docs/mcp-clients.md`](./mcp-clients.md).

**P6 — Claude Code rewiring. ✅ done.** Skills delegate mechanics to the tools; the
`${CLAUDE_SKILL_DIR}` and `${CLAUDE_PLUGIN_ROOT}` couplings are gone; `card.py` no longer
ships; the hook is untouched. The plugin bundles the server through `mcpServers` in `plugin.json` and a
launcher that prefers a local build and falls back to npx.
*Done:* proven by a cross-process contention test — and it failed first, see below.

**P7 — distribution.** `npx @alexparco/mnemo-mcp`. The README rewrite and the per-client
install snippets landed in P6 and P5, so what remains is packaging: a `prepack` that
builds (publishing from a clean checkout would otherwise ship an empty `dist`), the
missing npm metadata (`repository`, `homepage`, `bugs`, `publishConfig`, `keywords`),
aligning the package version with the plugin's, deciding whether source maps ship, and
the publish itself — which needs the author's npm account.

Publishing is not urgent: the launcher prefers a local build, so the plugin works today
without npm. It buys one thing, removing a manual build step per machine. A beta
`dist-tag` is the right shape when it happens, with one catch: `npx <pkg>` with no
version resolves `latest`, which `--tag beta` never sets, so the launcher has to name
the tag.

**Its blocking bug is already fixed** — see below. A package that installs cleanly and
does nothing is worse than no package.

**P8 — the mailbox: agent-to-agent messages across machines and vendors.** The end
goal. Claude Code's own `SendMessage` reaches only Claude Code sessions; nothing reaches
a Codex or opencode chat, and nothing crosses machines without Remote Control. MCP is
the one protocol all four speak, so the mailbox is a set of tools on the same server,
served remotely over Streamable HTTP. Full design in [P8 — the mailbox](#p8--the-mailbox).
*Done when:* a Claude Code chat on one machine leaves a `task` for a named Codex chat on
another, that chat receives it with `mailbox_wait`, replies `done`, and the reply lands
back — over an SSH tunnel, with the token enforced, and with two chats of the same
product on one machine addressed separately.

## What P0 + P1 landed

```
server/src/store/    text · frontmatter · paths · pending · memory · project · lock · store
server/src/card/     render (card.py port) · cli (drop-in replacement)
server/src/git/      read (status only; mutations are P3)
server/src/tools/    read (the 5 read-only tools)
server/src/index.ts  McpServer over stdio
server/test/         88 tests, incl. 16 byte-for-byte card comparisons
```

Both gates hold. The card port matches `card.py` byte for byte across
4 fixture projects × 2 languages × 2 machine identities, and a mutation check
confirms the gate bites: switching truncation from code points to UTF-16 units
breaks exactly the four cases whose fixture text contains an emoji.

### Decisions taken while building

- **Tool output is plain text, no `outputSchema`.** Structured output is unevenly
  supported across clients; a text block works everywhere. Revisit in P5.
- **The lock lives in `$XDG_STATE_HOME/mnemo/locks/`, not in the store** — keyed by
  the store's real path. Nothing new appears in `git status`, and it works against a
  read-only checkout. It reclaims a lock whose holder pid is gone rather than waiting
  out the 60s staleness window.
- **Search matches on all terms, not as one substring.** Ids are kebab-case
  (`orion-rate-limit-invariant`) and queries are spaced ("rate limit"); a literal
  substring match misses the most common query there is.
- **The machine rule ships inside the tool response.** `mnemo_load_project` returns
  the card, the detail, and then the "do not act on another machine's work" rule as
  its own block. With no SKILL.md outside Claude Code, the response is the only
  channel that reaches every host — see the behaviour-layer limitation above.

### What P2 changed about the planned tool surface

- **`mnemo_bootstrap` is a real tool**, not only an implicit step. The write tools
  still provision on their own, but a user setting up a second machine needs to be
  able to ask for it and see what happened.
- **`id` is required on `mnemo_write_memory`**, not optional as sketched above.
  Deriving an id from the body is a judgement call — "self-explanatory" is the
  SCHEMA's word — and code that invents slugs produces bad ones. The agent names it.
- **`overwrite` replaces the silent upsert.** Writing to an existing id refuses and
  says to read, merge, and retry with the flag. Silently replacing a memory's body
  is data loss, and the agent that hits this usually meant to create a new note.
- **`related` ids come back on every create**: memories sharing vocabulary with the
  new one. It is a nudge, not a block — the SCHEMA's "search for a duplicate first"
  rule, enforced as far as code can honestly enforce it.
- **No `mnemo_read_pending`**: `mnemo_load_project` already returns the parsed
  sections, and a second reader would be a second thing to keep in sync.

### A bug found by building it

The bootstrap sequence throws when a store that already has local content is
pointed at a hub that already has memory: `git checkout -B main --track origin/main`
aborts on the untracked `.gitignore` and `shared/SCHEMA.md`, and the whole write
fails with it. This is a documented user path — the README tells you to create the
store locally and wire `MNEMO_REMOTE` afterwards — and the shell block in
`save-context`'s SKILL.md has the same flaw.

Adoption is now best-effort, like the fetch above it: on failure the store stays
usable, the remote stays wired, and `adoptionBlocked` explains that the hub and
this store are two different memories whose merge is the user's call, not a side
effect of a save.

### A second bug, fixed on both sides

`card.py` kept the core section names in Python `set`s and iterated them.
String hashing is randomised per process, so a `pending.md` carrying **both**
language variants of a section rendered differently between runs — despite the
script's docstring promising determinism. Measured on one file, unchanged, run 40
times: two distinct outputs, 23 and 17. Past `MAX_PENDING = 5` it decided which
pending items were visible at all, and the card is the entire response the user
sees, so a task could simply vanish.

It is reachable through the project's own documented behaviour, not by accident:
`save-context` prescribes that a `pending.md` conflict is resolved as the **union**
of both sides, and SCHEMA.md says the old Spanish section names still work. Two
machines saving in different languages produce exactly the mixed file.

Both implementations now use ordered tuples, English first, and the
`mixed-tongues` fixture project pins the agreement in the parity test — a case
that could not be tested at all while the oracle was nondeterministic.

### What P3 decided

- **The secret scan has an acknowledgement, not a bypass.** A hard block with no
  way out bricks syncing on the first false positive; a `force` flag the agent can
  set is not a guard at all. `mnemo_push` returns the findings plus a value derived
  from HEAD and the exact findings, and only that value gets past. It is stateless,
  so it survives a restart, and it stops working the moment either changes. What
  code can guarantee is that the findings were surfaced; that a human read them is
  the tool description's job, and the description says so.
- **Findings are redacted.** They travel through an agent transcript, so echoing
  the secret verbatim would be its own leak. `file:line`, the rule name, and the
  match reduced to `AKI…LE` — enough to open the file, not enough to use.
- **Only added lines are scanned.** With the upstream as the base, those are
  exactly what the push would publish. Flagging removed or context lines would
  block every future push over one old leak. On a first push the base is the empty
  tree, so the whole history is scanned as additions.
- **One pattern was refined.** The plugin's `(password|secret|token|api_key)…[:=]`
  matches anything after the colon, which in a store made of engineering prose
  fires on ordinary notes. Measured against a ten-line prose corpus, one fires:
  `token: rotate every 15 minutes`. The rule now requires an unbroken 12+ character
  value. All seven rules are covered by a corpus with positives and negatives.

### The P2 open question, decided

`mnemo_commit` keeps `git add -A`, as the skills do. Writes and commits both take
the store lock, so a commit is atomic with respect to writes — the hazard is a
*split batch*, not corruption, and nothing is lost: the other agent's next commit
picks up the rest. A `paths` parameter that agents would not reach for is worse
than the documented behaviour. Two agents that genuinely need isolated batches
need separate worktrees, which is a different design.

### What P4 decided

- **The confirmation is a digest of the recomputed plan.** `apply` rebuilds the
  plan from the store and compares; any drift that matters — a memory that started
  tagging the slug after the plan, an edited `projects` field, the target already
  gone — changes the value and stops the operation. No server state, so it survives
  a restart, and it is single-use by construction: applying the plan changes the
  plan. Git state is deliberately excluded — a push between plan and apply changes
  nothing about what would be deleted.
- **Rename and forget make their own commit,** unlike the write tools. A structural
  change is one atomic thing, and its message (`forget(project x): 2 deleted, 1
  untagged`) is what keeps the history readable — which matters because git is the
  only undo. For the same reason they refuse to run on a dirty worktree rather than
  folding unrelated work into that commit.
- **Untag before delete.** If anything throws mid-operation, the shared memories —
  the side that must survive — are already safe.
- **Post-conditions are checked, not assumed.** After applying, no memory may still
  list the slug and the directory must be gone, read from parsed frontmatter. The
  skills ask the model to verify this and warn it not to grep the bare slug; here it
  is a check that throws.

### What P5 found, and what it changed

The unknown this plan flagged from the start is now measured — see
[`docs/mcp-clients.md`](./mcp-clients.md) for the matrix and the sources. Two
findings changed the design:

**Codex documents neither prompts nor resources, but it does read the server's
`instructions` field.** That inverts the priority the plan assumed. `instructions`
is not a courtesy header, it is the broadest channel mnemo has and the only one
documented for Codex, so it now carries the core criterion rather than a greeting
— short, because it is paid for on every connection. Prompts remain the richer
channel for the three clients that surface them.

**Codex's `default_tools_approval_mode = "writes"` prompts for tools not marked
read-only.** The `readOnlyHint` annotations turn out to be load-bearing: they are
what lets a user run in that mode and be asked about writes and pushes while loads
and searches pass unattended. A test asserts every tool carries the right hint.

The three channels — `instructions`, prompts, and the `mnemo_guide` snippet — are
all generated from `server/src/prompts/criterion.ts`, and a test asserts the rule
a tool returns is the same constant the guide contains. That test earned itself
immediately: it caught two different texts saying the same thing about
confirmation, one in the tools' responses and one in the guide. The long rule is
now composed from the short one.

### The bug that would have made P7 pointless

The entry point guarded startup with
``import.meta.url === `file://${process.argv[1]}` `` so that tests could import
`createServer` without launching a server. Node resolves a main module through
symlinks but leaves `argv[1]` as given, so the two differ whenever the file is
reached through a link — which is exactly how npm runs a `bin`. The published
package would have installed cleanly and then exited without a word. The same
comparison also broke on paths containing spaces.

The fix is to delete the question rather than answer it: `index.ts` builds a server
and has no side effects, `bin.ts` always runs one. No condition, no class of bug.

Four spawned tests cover it — real path, symlink, a directory with spaces, and that
importing the module still starts nothing. Restoring the old guard fails exactly the
two symlink cases and leaves the direct-path one passing, which is the bug's own
signature.

That control also had to be redone: the first attempt used `python3 -c "…"`, the shell
ate the escapes, `str.replace` matched nothing, and the suite reported all green for a
mutation that was never applied. A negative control that silently does not mutate is
indistinguishable from a test that does not discriminate.

### The bug P6 found, and the test that nearly missed it

The contention test — six processes writing and committing to one store — is the
phase's acceptance criterion. Written the obvious way it passed, and it was worth
almost nothing:

1. **It ran the workers serially.** `execFileSync` inside a `Promise` executor
   blocks, so `Promise.all` over six of them is six sequential runs. There was no
   contention to survive. Fixed with `spawn`.
2. **One write per worker still staggered**, on node's own startup cost. Each
   worker now loops several write+commit cycles so they genuinely overlap.

With both fixed the test failed **with the lock in place**, six times out of six,
on two different git errors: `cannot lock ref 'HEAD': is at X but expected Y`, and
`Unable to create '.git/index.lock': File exists`. Both mean two processes were
inside the store's git at once.

The cause was in the lock itself. `open(path, "wx")` creates an **empty** file and
only then writes the pid into it. A second process arriving inside that window
read no pid, concluded the holder was dead, and took the lock. The window is
microseconds; forty-eight acquisitions across six processes hit it reliably.

Publication is now atomic: the content is written to a temp name and `link()`ed
into place, so the lock file never exists without its pid. An unreadable lock is
also no longer assumed free — it is held until plainly abandoned. Six runs pass,
and six runs fail with the lock disabled, which is the part that makes the test
worth having.

The general lesson is the one the parity gate taught in P1: **a passing test for a
concurrency property is evidence of nothing until you have watched it fail.**

### What P6 changed about the plugin

- **The skills lost their mechanics.** No `git -C`, no `grep -rl`, no `mkdir -p`;
  they orchestrate `mnemo_*` tools and carry the judgement. A test asserts those
  shell patterns are gone, along with `CLAUDE_SKILL_DIR` and `CLAUDE_PLUGIN_ROOT`.
- **The skills are a fourth channel for the criterion, not a fourth copy.** Rule
  blocks are marked in the markdown and filled from `criterion.ts` by
  `server/scripts/sync-skill-rules.mjs`; a test asserts each block still equals its
  constant, so editing a rule in one place fails the build until the skills follow.
- **`card.py` moved to `server/test/oracle/`.** It no longer ships — python is not
  a runtime dependency any more — but deleting it would have thrown away the P1
  gate, which is the only reason the port is trustworthy. It stays as the oracle,
  with a header saying so.
- **The plugin bundles the server** via `plugin.json` (`mcpServers`) and `scripts/mnemo-mcp.sh`,
  which prefers `$MNEMO_MCP_COMMAND`, then a local `server/dist`, then npx. A
  plugin installed from git has no build step, so until P7 publishes the package
  the local build is the working path — and the launcher says exactly that when it
  finds neither, rather than leaving the skills calling tools that are not there.

### Divergences from `card.py`, both deliberate

1. **Dotfiles are included** in `memories/*.md`, because `pathlib.Path.glob` includes
   them and the card counts them. Verified rather than assumed.
2. **Truncation counts code points**, matching Python's `len`, not JS's UTF-16 units.

## P8 — the mailbox

### Why it is tools on the same server, over HTTP

The requirement is a message from any of the four agents to any other, on any
machine. The only thing the four have in common is MCP, and MCP's transports are
stdio and Streamable HTTP — there is no MCP-over-TCP or gRPC that any of them can
dial, and a protocol of our own would be one no client connects to. So: the mailbox is
six tools on the existing server, and the server grows an `--http` mode so a remote
machine can reach it. HTTP is not the mailbox's design choice; it is the only way a
remote MCP server exists.

The reference point is [Orca](https://www.onorca.dev/docs/cli/orchestration), which has
a working coordinator/worker orchestration across the same agents and hosts. What is
borrowed from it is listed per decision below; what is left out on purpose is the
product around it — runs, task DAGs, decision gates, worker heartbeats, worktrees,
terminals. mnemo is the memory the agents share plus a mailbox, with no UI.

### Surface

```
mailbox_register(name)                     claim a durable name for this session
mailbox_send(to, type, body, project?)     → id ; enqueues whether or not `to` is connected
mailbox_reply(id, type, body)              done / answer, bound to the original id
mailbox_inbox()                            what is waiting for me; advances my cursor
mailbox_wait(timeout_ms?)                  block server-side until a message or the timeout
mailbox_peers()                            names, product, connected/offline, unread
```

### Decisions

**Transport and exposure.** `--http` binds `127.0.0.1` by default; the laptop reaches
the VPS through `ssh -L`. An explicit `--bind` exists for Tailscale-style private
networks. Orca's rule, adopted verbatim in spirit: never forward the port to the public
internet. A bearer token (`MNEMO_TOKEN`, `Authorization: Bearer`) is **always** required
in HTTP mode, loopback included — it is cheap, and "add it later" is how it never gets
added.

**Identity, in three layers, first match wins.** (1) a request header
`X-Mnemo-Agent`, which Codex can source from an environment variable at launch
(`env_http_headers`) — two Codex chats with one config file get different identities
with zero agent cooperation; (2) `mailbox_register(name)`, for clients without
configurable headers or to rename mid-session; (3) the MCP `sessionId`, which
`RequestHandlerExtra` exposes to every tool handler and which is unique per connection
by construction. Layer 3 means a chat that never registered is still addressable; it
is ephemeral, so durable delivery keys on names, not ids. Over stdio there is no transport
session — `extra.sessionId` is undefined — but every stdio client spawns its own server
process, so there layer 3 is a per-process key and layer 1 is `MNEMO_AGENT` from the
client's `env` block, which stdio does honour.

**Name collisions are refused**, with the taken names listed, rather than "latest
wins". A stolen address sends messages to the wrong chat silently; a refusal is noticed.

**Groups**: `@all` and `@<product>` (`@codex`, `@claude-code`, `@opencode`), derived
from the `clientInfo` the handshake already carries. No `@idle`: there is no busy/idle
state and it will not be faked.

**Message types are a closed set**: `task | done | question | answer | note`.
`mailbox_reply` requires the originating `id` and only accepts `done` or `answer` —
Orca's lesson, that a completion must name the exact dispatch it closes so a stale
retry cannot close the wrong one.

**The brief is a pointer.** `mailbox_send(..., project: slug)` attaches the slug and one
line, not the full brief: the receiver shares the server and loads with
`mnemo_load_project`. Attaching everything would duplicate what it can already read.
This is where the earlier ideas — `detail: "card"`, a `service` filter, incremental
load — earn their place: several workers loading one project.

**Cursors belong to addresses, not connections**, borrowed from Orca's wire protocol
("resuming from the held cursor replays only what it missed"). Collisions are refused,
so a name has at most one live holder; a chat that restarts and reclaims its name
resumes from the name's cursor. That is what makes delivery survive a restart — a
per-connection cursor would not, because a stdio server is one process per chat and
dies with it. An unnamed chat reads with its session address's own cursor, which is
ephemeral like the address. (An earlier draft said two chats holding one name would
both see everything; that contradicts refusing the collision, and was dropped.)

**Retention.** Undelivered: forever. Delivered: 30 days, then pruned.

**`mailbox_wait`**: default 30 s, hard maximum 55 s. Codex's `tool_timeout_sec`
defaults to 60; Orca's 15-minute waits are possible only because Orca does not go
through MCP. The client docs say how to raise both if longer waits are wanted.

**State lives outside the store**: an append-only JSONL log plus a cursors file under
`$XDG_STATE_HOME/mnemo/mailbox/`, next to the locks. Never in `memories/` — messages
are ephemeral, memory is not. JSONL rather than SQLite: no native dependency, and
consistent with "plain text". Writes go through the existing store lock pattern.

**It also works over stdio, locally.** With file-backed state and the lock, two chats
on one machine message each other with no HTTP at all — each stdio client spawns its
own server process and they meet in the file. HTTP is only for crossing machines.

**Presence is reported with provenance.** `mailbox_peers` distinguishes a connection
seen live from a name restored from disk; Orca's `restoredUnconfirmed` rule — never
let hydrated state read as live truth.

### Prerequisite

Move the plugin's MCP declaration from the root `.mcp.json` into `mcpServers` in
`plugin.json`. Opening the mnemo repo itself as a project makes Claude Code read the
root file as project config, where `${CLAUDE_PLUGIN_ROOT}` is not substituted; the
server then fails with ENOENT on the literal path.

### Order of work

1. Prerequisite above. **✅ done.**
2. Mailbox state and the six tools over **stdio** — testable locally today, no new
   transport, and it settles the data model. **✅ done** — see below.
3. `--http` with the token; identity layers 1 and 3; `mailbox_wait`.
4. Empirical checks that only a real client can answer: whether each of the four keeps
   one MCP session for the life of a chat (layer 3 depends on it; layers 1–2 do not),
   and Codex's `env_http_headers` end to end.
5. Prompts and a `/mnemo:send` skill; client docs for the tunnel and the token.

### What steps 1–2 landed

`server/src/mailbox/store.ts` holds the state machine; `identity.ts` builds the caller
from the MCP request; `tools/mailbox.ts` is the surface. 20 new tests: 14 on the state
directly, and 6 end to end with **two real `bin.js` processes** driven by real MCP
clients over stdio — a task from a `claude-code` client reaches a `codex` client, the
`done` comes back bound to its id, and the product in `mailbox_peers` is read from
each client's handshake rather than assumed.

Four negative controls, each in its own copy of the tree, each failing exactly where it
should: removing the lock breaks the two-process burst (3 of 3 runs); not persisting
the name cursor breaks restart resumption; ignoring the group floor delivers old
broadcasts to late names; dropping the reply-type check lets a `done` close a
`question`.

Decisions settled by writing it:

- **The inbox tools are annotated read-only.** Reading advances a cursor, the way
  opening mail marks it read — the server's own bookkeeping, nothing the user owns.
  Annotating them as writes would make Codex's `writes` approval mode prompt on every
  inbox check, which defeats the habit the design depends on.
- **`seq` is taken from the log, not only from `state.json`.** A lost or stale state
  file must never hand out a seq again: a reused seq would let a reply bind to the
  wrong message. `nextSeq` is the maximum of both.
- **A torn last line is survivable.** A crash mid-append can leave a partial line; the
  next append starts on a fresh line so it does not corrupt the new message too, and
  the reader skips what it cannot parse.
- **Sending to a name nobody has registered is allowed, with a warning.** Refusing it
  would break the core case — leaving a task for a chat that is not open yet — but a
  typo would otherwise wait forever in silence.
- **`send` refuses `done` and `answer`.** Those only exist through `mailbox_reply`,
  where they must name the message they close.

### What is deliberately not in P8

Runs, task DAGs, decision gates, worker heartbeats, dispatch authority, worktrees,
terminal capture, version negotiation beyond what `tools/list` already gives. If a need
for any of them appears in use, it is a P9 conversation, not a P8 addition.

## Testing

- **Golden files** for the card (the P1 gate) — implemented as a live diff against
  `card.py`, not a checked-in snapshot, so the oracle cannot drift.
- **Mutation check**: a deliberate break must fail the parity gate. Run before
  trusting it. Two are on record: UTF-16 truncation breaks the four emoji-bearing
  cases, and inverting the core-section order breaks the four `mixed-tongues` ones —
  each hitting exactly the cases it should and no others.
- **Git integration** against a local bare repo: two clones, concurrent writes, rebase with a
  `pending.md` conflict, union merge.
- **Secret-scan corpus** — positives and negatives; a false negative is the worst bug
  in the repo. A test asserts the corpus covers every rule, so adding a rule without a
  sample fails. Sample secrets are assembled at runtime: a literal 40-character
  `ghp_…` in a committed file is a real token to a scanner, and would trip push
  protection on mnemo's own repo.
- **Store compat**: the fixture store is a v0.2-era store; every phase runs against it unmodified.
- **Decoys, not just happy paths**: the destructive suite carries a memory whose body
  writes `projects: [orion-api]` as prose and a project called `orion-api-v2`. Both
  must survive deleting `orion-api`. They are the two failures the skills work around
  with warnings, and the only way to know the parser really replaced the grep.
- **Hermetic env**: tests scrub `MNEMO_*` before running. Found the hard way — an
  ambient `MNEMO_REMOTE` pointed a throwaway store at the developer's real hub and
  fetched from it. Nothing was written there, but a test suite must never be able
  to reach it at all.

## Risks / open questions

1. ~~**MCP prompt support per client**~~ — measured, see above and
   [`docs/mcp-clients.md`](./mcp-clients.md). Claude Code, Cursor and VS Code surface
   prompts; Codex does not, and is reached through `instructions` instead.
2. **Elicitation** — Cursor documents it; Claude Code, VS Code and Codex do not.
   The plan/apply pattern was the right hedge and stays: it is the only confirmation
   mechanism that works on all four.
3. **Concurrency** was previously impossible and is now real. The lockfile is P0, not
   an afterthought — P2 showed it is not the whole story, and P6 found it was outright
   broken under real contention (see above). Driving the server with
   pipelined requests instead of awaited ones reorders them: a commit issued alongside
   writes lands before them and captures a partial batch. The lock serialises writes
   but does not order requests, and `mnemo_commit` stages the whole store with
   `add -A`, exactly as the skills do. A normal agent awaits each call and is fine;
   two agents sharing one store are not. Before P3 adds push, decide whether commit
   should stage only the paths the caller wrote.
4. **Card parity** python→TS: the machine flag regex and the bilingual section-icon matching are
   the fiddly parts.
5. **No session visibility** is structural, not a bug to fix. It must be stated in the README so
   nobody expects `save-context` to work as a bare tool call.
6. **Session stability per client (P8).** Whether Claude Code, Codex, Cursor and
   opencode hold one MCP session for the life of a chat is not documented by any of
   them. The identity design does not depend on it — headers and registration cover
   the case — but the `sessionId` address does. Verified only against real clients.
7. **`mailbox_wait` vs client tool timeouts (P8).** Codex defaults to 60 s; the others
   are undocumented. The 55 s cap is a guess at the tightest; measure each.
8. **The mailbox changes what mnemo is.** The README promises "zero infra". An HTTP
   mode with a token is infra, however small. The README has to say so the day P8
   lands, not after.
