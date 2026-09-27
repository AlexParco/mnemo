# Architecture

How the Go code is organised so that the specs in [specs/](specs/) can be built and changed safely.
There is no code yet; this is the plan the first commit follows.

## Principles

- **One binary, one module:** `github.com/AlexParco/mnemo`.
- **The domain does not know how it is reached.** Store, sync and mailbox code never imports the MCP
  SDK, HTTP, Cobra or the terminal. The same domain serves a stdio chat, the HTTP server and the
  terminal commands.
- **Definitions are data.** Tool names, descriptions, schemas, annotations and rule texts live in
  one place, and the local server, the relay and the plugin tests read them from there.
- **Everything outside mnemo is a process or a file.** Git, SSH, `claude`, `systemctl` and
  `launchctl` are run as processes, and tool configs are edited as files. Both are replaced by stubs
  in tests.

## Layout

```
cmd/mnemo/                 main: builds the root command and runs it
internal/cli/              Cobra commands; the only package that prints for people
internal/config/           config file, environment overlay, paths, machine label
internal/lock/             file lock plus in-process mutex, keyed by path
internal/memory/           pure parsing and rendering: frontmatter, pending, memories, projects,
                           overview, search, card
internal/store/            the Store: reads through memory, writes, bootstrap, rename, forget, commit
internal/gitx/             git runner, status, pull, resolve, rebase, push, secret scan
internal/mailbox/          the Mailbox: files, addresses, holders, delivery, retention, peek
internal/criterion/        rule texts, instructions, prompt texts, guide
internal/mcpserver/        tool and prompt definitions; local handlers over Store and Mailbox
internal/httpapi/          the server's HTTP handler: auth, /mcp, /v1
internal/remote/           client for /v1, relay, heartbeat, terminal mailbox client
internal/tunnel/           the SSH keeper and on-demand start
internal/service/          systemd unit and launchd plist: render, install, control
internal/integration/      claude, codex, opencode: add, remove, check, run
internal/hook/             suggest-save
internal/selfupdate/       release lookup, download, verify, replace
internal/testutil/         hermetic environment, fixtures, hubs, stubs, mutation runner
templates/                 SCHEMA.md and the package that embeds it
plugin/                    the Claude Code plugin
.claude-plugin/            marketplace catalogue
testdata/                  fixture store, expected cards, mutation patches
install.sh, install.ps1, .goreleaser.yaml
```

## Dependencies between packages

An arrow means "may import". Anything not listed may not.

| Package | May import |
|---|---|
| `cmd/mnemo` | `cli` |
| `templates` | standard library |
| `memory` | standard library |
| `lock` | `gofrs/flock` |
| `gitx` | `lock` |
| `store` | `memory`, `gitx`, `lock`, `templates` |
| `mailbox` | `lock` |
| `criterion` | standard library |
| `config` | `memory`, `BurntSushi/toml` |
| `mcpserver` | `store`, `gitx`, `mailbox`, `memory`, `criterion`, `config`, MCP SDK |
| `httpapi` | `mcpserver`, `mailbox`, `config`, MCP SDK |
| `remote` | `mcpserver`, `mailbox`, `config`, `tunnel`, MCP SDK |
| `tunnel` | `config`, `lock` |
| `service` | `config` |
| `integration` | `config`, `mailbox`, `remote`, `sjson`/`gjson`, `BurntSushi/toml` |
| `hook` | `config` |
| `selfupdate` | `config`, `service`, `tunnel` |
| `cli` | all of the above, Cobra |
| `testutil` | anything |
| `archtest` | standard library |

- **The mailbox does not import the store.** It needs to know whether a project exists, and receives
  that as a function.
- **A test enforces this table.** `internal/archtest` lists every package's direct imports with
  `go list -json ./...`. It fails on an import the table does not allow, and on a package the table
  does not declare, so a new package, subpackages included, must be added here first. The standard
  library is always allowed. Test files are not checked, so tests may import `testutil`.

## Main types

```go
// store
type Store struct { /* dir, clock, git runner, lock */ }
func (s *Store) Overview() (Overview, error)
func (s *Store) LoadProject(slug string, lang Lang, machine string) (ProjectContext, error)
func (s *Store) WriteMemory(in WriteMemoryInput) (WriteMemoryResult, error)
func (s *Store) PlanForget(kind Kind, target string) (Plan, error)
func (s *Store) ApplyForget(kind Kind, target, token string) (ForgetResult, error)
// …one method per operation in specs/store.md and specs/sync.md

// mailbox
type Caller struct { Name, Machine, Tool, Instance string; PID int; Via Via }
type Mailbox struct { /* dir, clock, rand, lock, projectExists */ }
func (m *Mailbox) Send(c Caller, in SendInput) (SendResult, error)
func (m *Mailbox) Read(c Caller) (ReadResult, error)
func (m *Mailbox) Heartbeat(c Caller) (Lease, error)
func Peek(dir, address string, after int64) (PeekResult, error)

// refusals: errors that carry the text an agent acts on
type Refusal struct { Message string }
```

- **`mcpserver.Definitions()`** returns every tool and prompt as data.
- **`mcpserver.NewLocal(store, mailbox, caller)`** registers each definition with a handler that
  decodes arguments, calls the domain and renders the texts in [specs/mcp.md](specs/mcp.md).
- **`remote.NewRelay(cfg, caller)`** registers the same definitions with handlers that forward the
  call.
- **Tools are registered with the SDK's low-level `AddTool`** and explicit JSON schemas, so both
  servers publish identical schemas. Arguments are validated against the schema before any handler
  runs.

## How a call flows

**Local chat.** The tool starts `mnemo serve`. `cli` builds the caller from the environment, opens
the Store and the Mailbox for this machine, and runs `mcpserver.NewLocal` over the stdio transport.
A tool call decodes, takes the lock if it writes, runs, and renders text.

**Server.** The service runs `mnemo serve --http`. `httpapi` authenticates, builds the caller from
the headers for each request, and hands `/mcp` to the SDK's Streamable HTTP handler in stateless
mode. Handlers read the headers from the request data the SDK passes them. `/v1` endpoints call the
Mailbox directly.

**Connected chat.** The tool starts `mnemo serve`. `cli` sees a connected remote and runs
`remote.NewRelay` over stdio. The relay answers the handshake and lists locally, forwards tool calls
through an SDK client session with a header-adding HTTP transport, and sends heartbeats. `tunnel`
makes sure the forward exists.

**Terminal.** `mnemo send`, `reply`, `read` and `peers` build a terminal caller. In local mode they
call the Mailbox directly. In connected mode they call the mailbox tools through the relay client
and render the structured content.

## Concurrency

- **Across processes**, every store and mailbox write takes the lock for its directory.
- **Inside the HTTP server**, requests run concurrently. The in-process mutex in `lock` serialises
  them per directory before the file lock.
- **Reads take no lock.** Atomic renames keep them consistent.
- **The domain holds no long-lived state in memory.** Every operation reads the files it needs, so
  any number of processes can share a store.
- **Background loops**, meaning the heartbeat, `watch` and the tunnel keeper, stop through a context
  cancelled on signals or on stdin closing.

## Errors

- **`Refusal`** is what an agent or a person should act on. MCP handlers return it as `isError` text,
  and `cli` prints it as `mnemo: <message>` with exit status 1.
- **Git failures** are `gitx.Error`, carrying the arguments and git's stderr. **A lock timeout** is
  `lock.ErrTimeout`. Both reach the agent as refusals.
- **Anything else** is unexpected. MCP handlers return `mnemo failed unexpectedly: <message>`, and
  `cli` prints it and exits 1.
- **Usage errors** exit with status 2.

## Time, randomness and environment

- **Clocks and random sources are fields**, set to the real ones in production and to fixed ones in
  tests.
- **Environment and paths** are read once, into a `config.Resolved` value that is passed down. No
  package below `cli` reads environment variables, except `hook`, which is a standalone entry point.

## Text and JSON

- **Truncation** counts runes, so an emoji costs one character of a budget, not four.
- **Sorting** compares strings byte-wise, which is code-point order for valid UTF-8.
- **JSON for agents** is encoded with two-space indentation and HTML escaping turned off, from
  structs whose field order is the documented order.

## Embedding

- **`templates/SCHEMA.md`** is embedded by the `templates` package. Bootstrap writes it, and a test
  checks that it equals the file in the repository.
- **Rule texts** are one Markdown file each in `internal/criterion/rules/`, embedded and exposed as
  variables of the same name. They are data, not code: a person edits a rule without reading Go, and
  what ships is byte for byte what the file says. A text carries another with `{{NAME}}`, so the
  short note a tool returns and the long rule in a guide cannot drift apart. The skills in `plugin/`
  carry marked copies, and a test checks they are identical.

## Libraries

| Library | Use | Version |
|---|---|---|
| `github.com/modelcontextprotocol/go-sdk` | MCP server, client, stdio and Streamable HTTP transports | v1.8.0, in the module cache here |
| `github.com/gofrs/flock` | cross-process file lock on Unix and Windows | v0.13.1, in the module cache here |
| `github.com/BurntSushi/toml` | reading the config file, validating Codex's file | v1.6.0, in the module cache here |
| `github.com/spf13/cobra` | commands, help, completion | latest at implementation time |
| `github.com/tidwall/sjson`, `gjson` | editing opencode's JSON while keeping its formatting | latest at implementation time |

- **Go:** 1.25. The version installed here is 1.25.1.
- **System git** is required at run time. go-git cannot rebase and only fast-forwards on merge.
- **SSH** is required only on connected clients.

## Build order

1. `memory`, `lock`, `config` and `templates`, with their tests and the card's expected files. The
   card is the first thing a person sees, so its layout is settled before anything is built on top.
2. `gitx` and `store`, with the fixture store and hub tests.
3. `criterion` and `mcpserver` local handlers, and `mnemo serve` over stdio. At this point the
   binary is usable: one machine, memory only, no mailbox. **Done**: all seventeen memory tools.
4. `mailbox`, with its tools, `mnemo peers`, `send`, `reply` and `read` in local mode, and `mnemo
   watch`.
5. `integration`, `plugin/` and `hook`, with `mnemo mcp add`, `remove` and `run`.
6. `httpapi`, `service` and `mnemo server`.
7. `tunnel` and `remote`, with `mnemo connect`, `disconnect` and the relay.
8. `selfupdate`, the install scripts and GoReleaser.
9. The negative control runner and the full mutation set.
