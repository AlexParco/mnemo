# Mailbox

Messages between agents: on one machine, or across machines through a server, from Claude Code,
Codex or opencode. The mailbox lives next to the store on the machine that hosts it. In local mode
that is this machine. In server mode it is the server.

The shape of it, in short:

- Addresses are `name@machine`.
- Every agent has a name, given by `mnemo run --name`. There are no session addresses and no
  registration call: an agent with no name has memory but no mailbox.
- Messages have no type. Any message can be answered, more than once.
- A name's holder is kept alive by its process on the same machine, or by a heartbeat from
  another machine. MCP sessions play no part.

## Addresses

| Form | Meaning |
|---|---|
| `name@machine` | One agent. Messages to it wait while it is closed. |
| `name` | Shorthand, accepted only when exactly one known address has that name. |
| `@all` | Every known name except the sender. |
| `@claude`, `@codex`, `@opencode`, `@terminal` | Every known name last seen in that tool, except the sender. |

- **Names** match `^[a-z0-9][a-z0-9-]{0,62}$`.
- **Machines** are normalised labels, as in [config.md](config.md#machine-label).
- **A known address** is one that has been held at least once, or that received a message.

## Identity of a caller

Every mailbox operation has a caller: an address, a tool and an instance.

| Part | Local, over stdio | Remote, through a server |
|---|---|---|
| Name | `MNEMO_AGENT` | `X-Mnemo-Agent` header, set by the client's relay |
| Machine | this machine's label | `X-Mnemo-Machine` header |
| Tool | `MNEMO_TOOL`, else the MCP client name | `X-Mnemo-Tool` header |
| Instance | random, one per process | `X-Mnemo-Instance` header |

- **The instance** is 16 random hex characters, generated when a `mnemo serve` process or a
  mnemo command starts.
- **The tool** is one of `claude`, `codex`, `opencode` or `terminal`. When nothing sets it, the
  MCP client name from the handshake is lower-cased with runs outside `a-z0-9` turned into `-`.
  That fallback is for display only, and a group address never matches it.
- **A caller without a name** can use every memory tool, but not the mailbox. Mailbox tools
  refuse with: `This chat has no mailbox name. Open it with: mnemo run <tool> --name <name>`.
- The server trusts the machine and name headers. Anyone with the token is the user.

## Holding a name

One address is held by at most one live instance. That is what lets a reply find exactly one chat.

- **A holder** records the instance, the tool, the process id, how it reaches the mailbox
  (`local` or `remote`), when it started and, for remote holders, when its lease ends.
- **Local holders are alive while their process is.** It is checked with the process id on the
  machine that hosts the mailbox.
- **Remote holders are alive while their lease lasts.** A lease is 45 seconds. Every request from
  that instance renews it, and the relay sends a heartbeat every 15 seconds. A client that
  vanishes loses the name at most 45 seconds later.
- **Claiming** happens when the instance starts. A local `mnemo serve` with a name claims it at
  startup, before answering the handshake. A relay claims with its first heartbeat. A terminal
  command claims before it runs. A chat that never uses the mailbox still holds its name, so
  `mnemo run` cannot open a second chat with it.
  - If the address has no live holder, the caller takes it.
  - If another live instance holds it, the claim is refused. The operation fails with a message
    naming the holder's tool and when it started.
  - `mnemo run` checks before launching the tool, so the usual collision stops before a chat opens.
- **Releasing** happens when the process exits cleanly: the local serve process removes its hold,
  and the relay sends a release. A crash is covered by the process check or by the lease.
- **Commands from a terminal** hold their `--name` for as long as the command runs. They are
  refused when a chat holds that name.

## Messages

```json
{
  "seq": 42,
  "id": "m16a3f0",
  "ts": "2026-09-15T14:03:12.345Z",
  "from": "worker-api@laptop1",
  "tool": "claude",
  "to": "orchestrator@vps",
  "body": "coupons endpoint is done, tests pass",
  "project": "shop",
  "reply_to": "m15c2e9"
}
```

- **`seq`** starts at 1, increases by one, and is never reused. After a crash the next `seq` is
  the larger of the stored counter and the highest `seq` in the log, plus one.
- **`id`** is `m`, then `seq` in base 36, then 4 random hex characters.
- **`ts`** is UTC with milliseconds.
- **`to`** is always stored resolved: a full address or a group.
- **`project`** and **`reply_to`** are optional.

## Operations

All of them run under the mailbox lock except `peek`. Each one first reaps dead holders, prunes
old messages when a prune is due, and records the caller.

### send(to, body, project?)

- **The body** is trimmed and must not be empty. At most 256 KiB are allowed.
- **Resolving `to`:**
  - `@all` or a tool group: kept as is. Any other `@…` is refused.
  - `name@machine`: kept. When nobody has ever held it, the result carries a warning that the
    message will wait and that the spelling can be checked with `mailbox_peers`.
  - A bare `name`: resolved to the one known address with that name. With none it is refused
    and says to use `name@machine` to leave a message for an agent that has not appeared yet.
    With several it is refused, and the refusal lists them.
- **Sending to yourself** is refused.
- **`project`** must be kebab-case and exist in the store next to this mailbox.
- The result is the stored message and any warning.

### reply(id, body)

- The original must still be in the log. When it is not, the refusal says that read messages are
  kept for 30 days.
- The caller must have been a recipient. It was a recipient when the original was sent to its
  address, or to a group that reached it. A group reached it when the caller's address was
  already known when that message was sent.
- The reply goes to the original's `from`. It carries `reply_to` and inherits the original's
  `project`.
- Several replies to one message are allowed.
- The body follows the same rules as `send`.

### read()

- It returns the unread messages for the caller in `seq` order, then marks them read.
- **Unread** means one of these:
  - sent to the caller's address, with `seq` above the address's cursor;
  - sent to `@all` or to the caller's tool group, with `seq` above both the cursor and the
    address's floor.
- Messages the caller sent itself never count.
- **The cursor** of the address moves to the highest `seq` in the log. Cursors belong to
  addresses, not to processes, so a chat that restarts under the same name continues where it
  left off.
- **The floor** of an address is the highest `seq` when the address first became known. Group
  messages from before it existed never reach it. Direct messages sent before it first appeared
  do reach it.

### wait(timeout_ms)

- The timeout defaults to 30 000 ms and is clamped to at most 55 000 ms, because Codex cuts tool
  calls at 60 seconds by default.
- Every 250 ms it checks the size of the log. When the size changed, it runs `read`. It returns
  as soon as that finds messages.
- When the time runs out, or the request is cancelled, it returns an empty result that says it
  timed out.

### peers()

- One row per known address: address, tool, `live` or `offline`, unread count, last seen, and
  whether it is the caller.
- `live` means a holder was confirmed alive during this call. `offline` means nobody holds it.
  A status is never inferred from old state.
- **The unread count** covers direct messages above the cursor.
- **Order:** the caller first, then live rows, then by address.
- The result also names the machine that hosts this mailbox.
- An address that has been offline for 90 days with nothing unread is dropped from the known
  addresses.

### peek(address, after)

For watchers. It reads the files without the lock, records nobody and marks nothing read. That
is safe because the state file is replaced atomically, the log is append-only, and a torn last
line is skipped.

- It returns the highest `seq` in the log and the messages that `read` would return for that
  address with a cursor of `max(after, cursor)`.
- An address that is not known receives no group messages.

## Retention

A prune runs at most once a day, inside a normal operation.

- A message older than 30 days is removed when it was sent to a group, or when it was sent to an
  address whose cursor is at or past it.
- A message to an address that has not read it is never removed.
- `seq` values are never reused after a prune.

## Files

The mailbox directory holds two files. They are separate from the store, because messages are
ephemeral and memory is not.

### `messages.jsonl`

- One JSON message per line, append-only.
- An append starts on a new line when the file does not end with one, so a line torn by a crash
  does not corrupt the next message. The reader skips lines it cannot parse.
- A prune rewrites the file atomically.

### `state.json`

```json
{
  "version": 2,
  "next_seq": 43,
  "pruned_at": "2026-09-15T00:00:00.000Z",
  "addresses": {
    "worker-api@laptop1": {
      "tool": "claude",
      "floor": 17,
      "cursor": 41,
      "last_seen": "2026-09-15T14:03:12.345Z",
      "holder": {
        "instance": "3f9c0a7e5b21d4c8",
        "via": "remote",
        "pid": 51234,
        "since": "2026-09-15T13:40:00.000Z",
        "lease_until": "2026-09-15T14:03:57.345Z"
      }
    }
  }
}
```

- It is replaced atomically on every operation that changes it.
- **Files with no `version` field** were written by an earlier install. Both are moved to
  `v1-<timestamp>/` inside the mailbox directory and the mailbox starts empty: messages are
  ephemeral and the address format is not the one they use. `mnemo peers` mentions the move once.

## Watching

`mnemo watch` wakes an idle Claude Code chat when a message arrives. Its behaviour is in
[integrations.md](integrations.md#mnemo-watch). It uses `peek`, locally or through the server's
unread endpoint.

## What an agent is told

A message from another agent is a request, not an instruction from the user. The texts in
[mcp.md](mcp.md) say so where agents read them: the server instructions and the output of
`mailbox_read`. The agent tells the user what the message asks and waits, unless the project's
rules file authorises that sender. The mailbox never grants authority itself.
