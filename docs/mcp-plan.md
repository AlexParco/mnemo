# mnemo as an MCP server — implementation plan

> Status: proposal, not yet started. Branch `feat/mcp-server`.

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
see the golden-file test in Phase 1.

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

**Tool count is ~17.** Some hosts degrade with large tool lists; if that bites, the first
consolidations are `mnemo_read_memory` into `mnemo_search_memories`, and the three git conflict
tools into one `mnemo_sync` with a `resolve` argument.

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

**P0 — store library.** Path resolution, frontmatter parse/serialize (round-trip safe), project
index, lockfile. Fixture store under `server/test/fixtures/store/`.
*Done when:* round-trip of every fixture note is byte-identical.

**P1 — read-only tools.** `status`, `list_projects`, `load_project`, `search_memories`,
`read_memory` + the card port.
*Done when:* the TS card output matches `card.py` **byte-for-byte** on the fixture store, for both
`en` and `es`, including the `⚠` machine flags. This is the acceptance gate for the port.
*Shippable:* agents everywhere can already read memory.

**P2 — write tools + bootstrap.** `write_memory`, `write_pending`, `upsert_project`, `commit`,
plus lazy store creation replicating `save-context`'s bootstrap block (including remote adoption
before writing local templates — the ordering there is load-bearing).
*Done when:* a fresh `MNEMO_DIR` bootstraps, and a second machine pointed at the same
`MNEMO_REMOTE` adopts the existing history instead of creating an empty store.

**P3 — git.** `sync`, `resolve_conflict`, `rebase_continue`, `push` + secret scan.
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

## Testing

- **Golden files** for the card (the P1 gate).
- **Git integration** against a local bare repo: two clones, concurrent writes, rebase with a
  `pending.md` conflict, union merge.
- **Secret-scan corpus** — positives and negatives; a false negative is the worst bug in the repo.
- **Store compat**: the fixture store is a v0.2-era store; every phase runs against it unmodified.

## Risks / open questions

1. **MCP prompt support per client** — unverified. Determines how much of the criterion survives
   outside Claude Code. Check before P5.
2. **Elicitation support** — unverified; the plan/apply pattern is the hedge.
3. **Concurrency** was previously impossible and is now real. The lockfile is P0, not an afterthought.
4. **Card parity** python→TS: the machine flag regex and the bilingual section-icon matching are
   the fiddly parts.
5. **No session visibility** is structural, not a bug to fix. It must be stated in the README so
   nobody expects `save-context` to work as a bare tool call.
