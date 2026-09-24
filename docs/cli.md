# mnemo

mnemo is a shared memory and a mailbox for coding agents. Agents in different tools,
and on different machines, load and save the same project context and send messages
to each other.

It ships as a single binary. The binary carries the MCP server that agents talk to,
and the commands people use around it: registering mnemo in each coding tool,
launching agents, sending messages between them, and, when agents live on more than
one machine, running a server and connecting to it.

Memory still lives in the agents. Loading and saving project context happens from a
chat, through MCP tools. The commands are for people: setting things up, opening
agents, and talking to them. This document describes those commands. The detailed
behaviour behind them is in [specs/](specs/), and how the code is organised is in
[architecture.md](architecture.md).

## Vocabulary

- **Tool**: Claude Code, Codex or opencode. mnemo is registered in a tool once. These
  three are supported because they are the ones in use. Another tool is added when it is
  needed, as one more integration next to these.
- **Agent**: a running chat with a name, such as `worker-api@laptop1`. `mnemo run` starts one.

## Modes

**Local is the base.** With nothing but `mnemo mcp add`, every agent on a machine shares
memory and mailbox through files on that machine, ordered by an operating-system file
lock. Nothing runs in the background. This is enough to orchestrate several agents on
one machine.

**A server is a local mnemo that other machines can reach.** `mnemo server setup`
exposes a machine's local mnemo, and agents on that same machine keep working on its
files directly. Other machines **connect** to it, and then use the server's memory and
mailbox instead of their own.

There is **one server** at most. The config stores servers as a list, so a second one
can be added later without a migration, but only one is supported now.

| Role | Platforms |
|---|---|
| Local | Linux, macOS, Windows |
| Server | Linux, macOS |
| Client of a server | Linux, macOS, Windows |

## Retiring a store

A store that will never be used again is retired with `mnemo store remove`. It is the one
destructive operation no agent can perform.

- **It shows what would go first**: the path, every project with how many memories it holds, the
  total, whether there is a hub and how many commits have not reached it.
- **It asks the user to type the store's path** to go ahead. A yes or no is too easy to give by
  accident for something with no undo.
- **By default it moves the store aside**, to `<store>.retired-<date>`, and prints where it went and
  the one command that deletes it for good. `--purge` skips that and deletes it.
- **It never touches the hub.** If the store was pushed, the memory is still there, and that is the
  copy that outlives the machine. Removing the hub's copy is done on the hub, by its owner, and the
  command says so rather than doing it.
- **Unpushed work stops it.** With commits the hub has not seen, it refuses and says how many:
  retiring then would destroy the only copy. `--force` goes ahead anyway.
- **It leaves everything that is not the store**: the config file, the mailbox and the server keep
  working. Only the lock file belonging to that store is cleaned up.

Removing one project, or one memory, is a different thing and stays with the agent: that is
`mnemo_forget`, which shows what it would delete and waits for a yes.

## Whose memory this is

**The memory is the person's, not the project's.** One store holds every project, it lives outside
any of them, and it travels between the user's own machines. A fact that matters to two projects is
tagged with both and is written once.

The other answer exists and is a good one: memory kept inside a project's repository, committed with
the code, reviewed like any other file, and shared with whoever clones it. Tools that do that are
worth using for what they are. It is a different product, with a different owner, and mnemo is not
trying to be it.

What that choice buys: memory that survives a project nobody has pushed yet, notes that span
projects, and a store that is yours rather than your employer's. What it costs: a teammate does not
get your memory by cloning the repo, and there is no review step before something becomes shared.
mnemo's sharing is between your own machines and your own agents.

## Commands

```
SERVER, on the machine that stays on
mnemo server setup [--port N]           install and start the server as a service, generate the token, print the pairing code once
mnemo server restart                    restart with the same token
mnemo server rotate                     new token and pairing code, restart; connected clients must connect again
mnemo server remove                     stop and remove the service; the store and mailbox stay

CONNECTION, on each client machine
mnemo connect <code> [--ssh TARGET]     paste the pairing code, enter the SSH connection, save it, open the tunnel
mnemo connect                           reopen the tunnel from what is saved
mnemo disconnect                        close the tunnel and go back to local mode

TOOLS
mnemo mcp add                           register mnemo in every tool found: Claude Code, Codex, opencode
mnemo mcp add claude                    only one; for Claude Code this installs mnemo's plugin; same for codex and opencode
mnemo mcp add claude --path P           write to a specific file instead
mnemo mcp remove [claude]               remove mnemo's entry from one tool, or from all

AGENTS
mnemo run claude --name X [dir] [-- args]      open an agent with that name; args after -- go to the tool
mnemo run codex --name X [dir] [-- args]
mnemo run opencode --name X [dir] [-- args]

MAILBOX
mnemo peers [--name X]                  where the mailbox lives, and who is there: address, tool, live or offline, unread
mnemo send <to> "message" --name X [--project P]    send anything to another agent
mnemo reply <id> "message" --name X     answer a message, bound to its id
mnemo read --name X                     read the messages waiting for a name, and mark them read

STORE
mnemo store remove                      retire this machine's store: show what would go, then move it aside
mnemo store remove --purge              delete it outright instead of moving it aside

SETTINGS AND UPDATES
mnemo config                            every setting, its value and where it comes from
mnemo config set <key> <value>          machine, lang, store.dir, store.remote, store.autopush, mailbox.dir
mnemo config unset <key>
mnemo version
mnemo update [--version vX.Y.Z]         download, verify and replace the binary; restart what runs in the background

INTERNAL, hidden from help
mnemo serve [--http]                    the MCP server; over stdio for a tool, over HTTP for the service
mnemo watch --tool claude               the watcher that wakes an idle Claude Code chat
mnemo tunnel                            the background process that keeps the SSH tunnel open
mnemo hook suggest-save                 the save reminder run by the Claude Code plugin
```

- Errors go to stderr as `mnemo: <message>`.
- Exit status is 0 on success, 1 on failure and 2 on a usage error.

## Happy path, one machine

An orchestrator in Claude Code and two workers, one in Claude Code and one in Codex,
all on the same project, on one laptop, with no server.

**1. Once per machine**

```
$ mnemo mcp add
  claude     added (plugin mnemo@mnemo)
  codex      added (~/.codex/config.toml)
  opencode   skipped (not found)
```

**2. Open the agents, each in its own terminal**

```
$ mnemo run claude --name orchestrator ~/shop
$ mnemo run claude --name worker-api   ~/shop/api
$ mnemo run codex  --name worker-web   ~/shop/web
```

Without step 1, any of these stops and says to run `mnemo mcp add`.

**3. They share the same context**

The first time, the orchestrator is asked to save the context of `shop`. That creates
the project, with its decisions and pending work, in the machine's local memory. The
workers are asked to load the context of `shop`, and both read the same memory, even
though one is Claude Code and the other is Codex. A decision one of them saves is there
for the other the next time it loads or searches.

**4. See who is there**

```
$ mnemo peers
  mailbox on laptop1 (local mode)
  ADDRESS                TOOL     STATUS   UNREAD   LAST SEEN
  orchestrator@laptop1   claude   live     0        just now
  worker-api@laptop1     claude   live     0        just now
  worker-web@laptop1     codex    live     0        just now
```

**5. Hand out work**

The orchestrator is asked to send worker-api the task of adding the coupons endpoint,
and worker-web the task of building the form, both pointing at project `shop`.

- **worker-api** runs in Claude Code and is idle. The notification wakes it. It reads
  the message, asks the user before acting, does the work, and replies.
- **worker-web** runs in Codex, which has no monitors, so it does not wake on its own. It
  is asked to read its messages, and continues the same way from there.

**6. Replies come back**

Each reply reaches the orchestrator, which wakes for it. At the end, the context is saved
so the next session picks up from there.

**7. From a terminal, without opening a chat**

```
$ mnemo send orchestrator "how is the shop going?" --name alex
  sent m2k4f1 to orchestrator@laptop1 as alex@laptop1
$ mnemo read --name alex
  m2m9a0 · from orchestrator@laptop1 (claude) · 14:03 · in reply to m2k4f1
    endpoint and form done, deploy pending
```

One `mcp add`, and after that everything is `run`.

## Happy path, two machines

```
# on the server machine
$ mnemo server setup                              prints the pairing code
$ mnemo mcp add claude                            only if this machine also runs agents
$ mnemo run claude --name orchestrator ~/api      orchestrator@vps

# on a laptop
$ mnemo connect <code>                            asks for user@host, opens the tunnel
$ mnemo mcp add claude
$ mnemo run claude --name chat-a ~/web            chat-a@laptop1
$ mnemo run claude --name chat-b ~/docs           chat-b@laptop1
```

From chat-b, a task goes to `orchestrator`. The orchestrator chat is idle. The
notification wakes it, and it sees a message from `chat-b@laptop1`. It asks the user
before acting, does the work, and replies. The reply goes back to chat-b exactly, and
wakes it too.

From a terminal:

```
$ mnemo send orchestrator "check the deploy" --name alex
  sent m3a0c2 to orchestrator@vps as alex@laptop1
$ mnemo read --name alex
  m3a1f7 · from orchestrator@vps (claude) · 16:40 · in reply to m3a0c2
    deploy checked, all green
```

After a reboot the laptop needs no command. Opening an agent brings the tunnel back.

## Identity

- **Every address is `name@machine`.** A message says which machine and which chat
  sent it, and a reply goes back to exactly that address.
- **The name** comes from `--name`, which is required when running an agent. From a
  terminal it comes from `--name` or `MNEMO_AGENT`. With neither, the command stops.
- **The machine** comes from `MNEMO_MACHINE`, else the `machine` setting, else the
  hostname. It is normalised to lower-case letters, digits and dashes.
- **An address is held by one chat at a time.** A second chat asking for an address that
  is already held is refused, and so is a terminal command using the name of an open chat.
  `mnemo run` checks before opening the tool.
- **A chat opened without `mnemo run`** can use the memory, but not the mailbox. Its
  mailbox tools say to open it with `mnemo run`.
- **A bare name is enough when it is unambiguous.** `mnemo send orchestrator` works if
  there is one `orchestrator`. If there are several, the command stops and lists them. To
  leave a message for an agent that has never been open, use its full address.
- **The client sends its machine.** The server cannot know where a client runs, so the
  local `mnemo serve` includes it in every request.

## Server

- **It listens on 127.0.0.1 and requires the token on every request.** Another machine
  reaches it through an SSH tunnel, and the port is never opened to a network.
- **Linux:** a systemd user service with lingering enabled. It needs no sudo, runs as the
  user, starts at boot and survives restarts. Setup checks lingering and says so when it
  is off.
- **macOS:** a LaunchDaemon, which needs sudo to install. A LaunchAgent would not do,
  because it only runs while the user is logged in.
- **Windows as a server is out of scope for now.** `mnemo serve --http` can still be run
  by hand.
- **The service files are written directly**, not through a service library.
- **The service pushes to the hub without a terminal**, so git's SSH key must work
  unattended. Setup checks it and warns.
- **The server machine does not `connect`.** The server runs there already. It only runs
  `mcp add` if it will also run agents, and those agents work on its files directly.

## Connection

- **The pairing code carries the token, the port and the server's machine label.** It is
  a secret, printed once.
- **`connect` always asks for the SSH connection**: `user@host`, or an alias from
  `~/.ssh/config`. `--ssh` gives it without a prompt. Key-based SSH is required.
- **The tunnel is kept by a background process** that `connect` starts. Anything that needs
  the server starts it again when it is not running, so a reboot needs no command.
- **A connected machine never falls back to its own files.** When the server cannot be
  reached, the agent is told so and nothing is written locally.
- **Open chats keep the mode they started with.** After `connect` or `disconnect`, they
  switch when they are restarted.
- **A machine's local files are left untouched while it is connected**, and are in use again
  after `disconnect`. They are never merged into the server.
- **Memory a machine had before connecting is never merged automatically.** Sharing memory
  across machines keeps working through mnemo's git sync. The mailbox is never merged,
  because its messages are ephemeral.

## Tools and agents

- **`mcp add` writes to each tool's global MCP config**, and `--path` writes elsewhere.
- **The entry it writes has no URL, no token and no name.** It runs `mnemo serve` and says
  which tool it is. In local mode that process works on the machine's files. When
  connected, it reads the token from mnemo's own config, readable only by the user, and
  talks to the server. In both, it takes the agent's name from the environment that
  `mnemo run` sets. There is one `mnemo` entry per tool, and no secret in the tools' config.
- **`run` stops when `mcp add` is missing**, instead of fixing it silently. Being connected
  is not required: without a server, agents run in local mode. When connected, `run` stops
  if the server cannot be reached.
- **For Claude Code, `mcp add` installs mnemo's plugin** with `claude plugin marketplace add`
  and `claude plugin install`. The plugin brings the MCP entry, the monitor, the save
  reminder and the `/mnemo:*` commands, so no separate MCP entry is written for Claude Code.
  If the `claude` command is missing or the install fails, `mcp add` prints the commands for
  the user to run.
- **Only Claude Code wakes on its own**, through that plugin's monitor. Codex and opencode have
  no monitors, so they have to be asked to read their messages.
- **MCP is needed in every mode, and a server is not.** MCP is the only way an agent can use
  mnemo from a chat, and the only thing Claude Code, Codex and opencode have in common. In
  local mode it runs over stdio: each tool starts `mnemo serve` itself when a chat opens,
  with no port, no token and no background service, and the process ends with the chat.
- **Commands are for people and MCP is for agents.** An agent could run `mnemo read` in a
  shell, but a shell command does not carry the rules to the agent the way tool descriptions
  do, and each shell command may ask for permission.

## Mailbox

- **A message carries anything.** There is no message type and no limit on what one agent
  sends another: a task, a question, a note, a status, whatever the user or agent wants.
- **Any message can be answered**, as many times as needed. Each answer is bound to the id
  of the message it answers, so it cannot get attached to the wrong one.
- **A message to an address waits** even while that chat is closed.
- **Groups:** `@all` reaches every known agent, and `@claude`, `@codex`, `@opencode` or
  `@terminal` reaches every agent in that tool.
- **A notification is not an instruction.** The receiving chat asks the user before acting
  on a message from another agent. Authorising a sender, a whole machine or a single chat,
  is the user's decision, made in that project's rules file, such as `CLAUDE.md` or
  `AGENTS.md`.
- **The MCP tools use the same verbs as the commands:** `mailbox_peers`, `mailbox_send`,
  `mailbox_reply`, `mailbox_read`, and `mailbox_wait` for agents that cannot be woken.
- **`peers` always says where the mailbox lives**, local or on a server, so there is never
  doubt about which mailbox you are looking at.

### Output from a terminal

```
$ mnemo peers --name alex
  mailbox on vps (connected through me@vps)
  ADDRESS            TOOL       STATUS    UNREAD   LAST SEEN
  alex@laptop1       terminal   offline   0        13:55     (you)
  orchestrator@vps   claude     live      0        just now
  chat-a@laptop1     claude     live      2        14:01

$ mnemo send worker-api "rebase on main before merging" --name alex --project shop
  sent m4b2e8 to worker-api@laptop1 as alex@laptop1

$ mnemo read --name alex
  m4c010 · from worker-api@laptop1 (claude) · 14:12 · in reply to m4b2e8 · project shop
    rebased, CI green

$ mnemo read --name alex
  no new messages for alex@laptop1
```

Times are local. A message from an earlier day shows its date.

## Settings

- **Settings live in `~/.config/mnemo/config.toml`**, written by mnemo commands, with mode
  `0600` because it can hold the server token.
- **An environment variable overrides the file**, and a flag overrides both. `MNEMO_DIR`,
  `MNEMO_REMOTE` and the rest are listed in [specs/config.md](specs/config.md).
- **`mnemo config`** shows each value and where it came from. It never prints a token.

## Installation

- **Binaries for Linux, macOS and Windows**, built with GoReleaser and published as GitHub
  Releases with checksums.
- **A one-line install script**: `curl -fsSL .../install.sh | sh` on Linux and macOS, and
  `irm .../install.ps1 | iex` on Windows. It downloads the binary for the platform, checks it
  against the checksums, and puts it in `~/.local/bin`, or `%USERPROFILE%\.local\bin` on
  Windows, which is where Claude Code puts its own. It says so when that directory is not on
  the `PATH`.
- **`mnemo update`** replaces the binary with a verified release, restarts the server service
  and the tunnel when they run, and updates the Claude Code plugin.
- **Later**: Homebrew, Scoop and Winget from the same GoReleaser setup. `go install` works for
  Go users from the start.

## Publishing

This project replaces the current mnemo repository when it is finished. The Go module path
must use GitHub's canonical capitalisation, `github.com/AlexParco/mnemo`, because Go module
paths are case-sensitive.

## Out of scope for now

- Tools other than Claude Code, Codex and opencode.
- More than one server.
- Windows as a server.

## Checks during implementation

Every decision in these documents is taken. These are not open questions. They are
behaviours of other tools that can only be confirmed by running them, and each one gets
checked when the part that depends on it is built.

1. **Claude Code plugin commands.** `claude plugin marketplace add`, `install`, `update` and
   `uninstall` must run end to end without interaction. How `claude plugin list` shows an
   installed and enabled plugin also needs checking.
2. **Claude Code plugin runtime.** The `env` block of an MCP server in `plugin.json` must be
   applied. The MCP server, monitor and hook must find `mnemo` on the PATH. The MCP server
   must inherit `MNEMO_AGENT` from the chat's environment, as the monitor was seen to.
3. **opencode.** Its local MCP server must inherit `MNEMO_AGENT`. It is unknown whether the
   global config follows `XDG_CONFIG_HOME` or only `~/.config`.
4. **Codex.** `env_vars` must forward `MNEMO_AGENT`, and an inline `env` table must be
   accepted. It is unknown whether `CODEX_HOME` moves `config.toml`.
5. **The Go MCP SDK, v1.8.0.** The stateless Streamable HTTP handler must work behind the
   relay's client session, with its standalone event stream disabled. Request headers must
   reach tool handlers in stateless mode.
6. **The MCP client names** each tool sends in its handshake, which only name the tool when
   nothing else does.
7. **Windows clients.** The tunnel keeper must run detached, a running `mnemo.exe` must be
   replaceable by renaming it, and process-id liveness checks must hold.
8. **Pushing from the service.** Git over SSH must work from a systemd user service and from
   a LaunchDaemon, without a terminal.
