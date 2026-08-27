# mnemo as an MCP server — implementation plan

> Status: **P0–P3 landed** (store library, read tools, write tools + bootstrap, sync/push
> + secret scan; 14 tools, 164 tests green). Branch `feat/mcp-server`. P4–P7 still proposal.

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
| `mnemo_rename_plan` | `old`, `new` | impact (dir, INDEX, affected memories) + `token` |
| `mnemo_rename_apply` | `token` | applies; refuses without a valid, fresh token |
| `mnemo_forget_plan` | `kind: project\|memory`, `id` | for a project: **exclusive** (delete) vs **shared** (untag) split + `token` |
| `mnemo_forget_apply` | `token` | applies |

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

**P4 — destructive.** `rename_plan/apply`, `forget_plan/apply`.
*Done when:* deleting a project leaves every shared memory alive and correctly untagged, verified
by reading `projects:` fields, not by grepping the slug.

**P5 — behavior layer.** MCP prompts + `mnemo_guide` + per-client `mcp.json` examples.

**P6 — Claude Code rewiring.** Skills delegate mechanics to the tools; drop `${CLAUDE_SKILL_DIR}`
and `${CLAUDE_PLUGIN_ROOT}` couplings; `card.py` removed once P1 parity holds; hook untouched.
*Done when:* the plugin and the server can be used against the same store on the same machine
without corrupting it (lockfile under contention).

**P7 — distribution.** `npx @alexparco/mnemo-mcp`, README rewrite, install snippets per client.

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

### Divergences from `card.py`, both deliberate

1. **Dotfiles are included** in `memories/*.md`, because `pathlib.Path.glob` includes
   them and the card counts them. Verified rather than assumed.
2. **Truncation counts code points**, matching Python's `len`, not JS's UTF-16 units.

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
- **Hermetic env**: tests scrub `MNEMO_*` before running. Found the hard way — an
  ambient `MNEMO_REMOTE` pointed a throwaway store at the developer's real hub and
  fetched from it. Nothing was written there, but a test suite must never be able
  to reach it at all.

## Risks / open questions

1. **MCP prompt support per client** — unverified. Determines how much of the criterion survives
   outside Claude Code. Check before P5.
2. **Elicitation support** — unverified; the plan/apply pattern is the hedge.
3. **Concurrency** was previously impossible and is now real. The lockfile is P0, not
   an afterthought — and P2 showed it is not the whole story. Driving the server with
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
