# Config

Where mnemo keeps its settings and its data, and how a setting is resolved.

## Files and directories

Every path below uses the same defaults on Linux, macOS and Windows. On Windows `~` is
`%USERPROFILE%`.

| What | Path |
|---|---|
| Config file | `$MNEMO_CONFIG`, else `$XDG_CONFIG_HOME/mnemo/config.toml`, else `~/.config/mnemo/config.toml` |
| Store | `$MNEMO_DIR`, else `store.dir`, else `$XDG_DATA_HOME/mnemo`, else `~/.local/share/mnemo` |
| State directory | `$XDG_STATE_HOME/mnemo`, else `~/.local/state/mnemo` |
| Locks | `<state>/locks/` |
| Mailbox | `$MNEMO_MAILBOX_DIR`, else `mailbox.dir`, else `<state>/mailbox/` |
| Tunnel keeper | `<state>/tunnel/` |
| Logs | `<state>/logs/` |
| Save reminder state | `<state>/suggest-save/` |

- An environment variable set to an empty string counts as unset.
- Relative paths from the environment or the config file are resolved against the current
  directory once, when the setting is read, and used as absolute paths from then on.
- The config file is created with mode `0600` and its directory with `0700`. It can hold the
  server token. On Windows the file inherits the user profile's ACL, which already limits it
  to the user.

## Format

TOML, written only by mnemo commands. A user may edit it by hand; mnemo reads it on every
command and on every `mnemo serve` start. Unknown keys are kept when mnemo rewrites the file.

```toml
machine = "laptop1"          # optional; see Machine label
lang = "es"                  # optional; en or es

[store]
dir = ""                     # optional
remote = "user@vps:mnemo.git"
autopush = false

[mailbox]
dir = ""                     # optional

[server]                     # written by `mnemo server setup` on the server machine
port = 7433
token = "…"

[[remotes]]                  # written by `mnemo connect` on a client machine
machine = "vps"              # the server's machine label, from the pairing code
ssh = "user@vps"             # the SSH target the user entered
server_port = 7433           # from the pairing code
local_port = 7433            # chosen by connect
token = "…"                  # from the pairing code
connected = true             # false after `mnemo disconnect`
```

- `remotes` is a list so a second server can be added later without a migration. Only one
  entry is supported now: `connect` with a different code replaces the existing entry after
  asking.
- The file is written atomically: a temporary file in the same directory, then a rename.

## Resolution

For every setting the first match wins:

1. A command-line flag, where the command has one.
2. The environment variable.
3. The config file.
4. The default.

| Setting | Environment | Config key | Default |
|---|---|---|---|
| Machine label | `MNEMO_MACHINE` | `machine` | the short hostname |
| Language | `MNEMO_LANG` | `lang` | `en` |
| Store directory | `MNEMO_DIR` | `store.dir` | see the table above |
| Hub remote | `MNEMO_REMOTE` | `store.remote` | none |
| Push without asking | `MNEMO_AUTOPUSH` | `store.autopush` | off |
| Mailbox directory | `MNEMO_MAILBOX_DIR` | `mailbox.dir` | see the table above |
| Config file | `MNEMO_CONFIG` | none | see the table above |
| Agent name | `MNEMO_AGENT` | none | none |
| Tool | `MNEMO_TOOL` | none | none |

- `MNEMO_AUTOPUSH` is on for any non-empty value except `0`, `false` and `no`, compared
  case-insensitively. `MNEMO_AUTOPUSH=0` means off, which is the only reading of it a person
  ever intends.
- `MNEMO_AGENT` and `MNEMO_TOOL` are never stored. `mnemo run` sets `MNEMO_AGENT`, and each
  tool's MCP entry sets `MNEMO_TOOL`.

## Machine label

The machine label names this machine in addresses (`name@machine`) and in pending items
stamped `[@machine]`.

- Source: the resolved setting above, else the hostname up to its first `.`.
- Normalisation: lower-case; Latin letters with diacritics fold to their base letter, so `Ñandú`
  gives `nandu`, `ß` gives `ss` and `æ` gives `ae`; then every run of characters outside `a-z`, `0-9`
  and `-` becomes one `-`; leading and trailing `-` are removed; at most 63 characters.
- If the result is empty, every command that needs it stops and says to set `machine` with
  `mnemo config set machine <label>`.
- Two machines with the same label are the user's problem to avoid. `mnemo connect` refuses
  when the client's label equals the server's, because every address would collide.

## `mnemo config`

```
mnemo config                         print every setting, its value and where it came from
mnemo config set <key> <value>       write a key to the config file
mnemo config unset <key>             remove a key from the config file
```

- Keys that can be set: `machine`, `lang`, `store.dir`, `store.remote`, `store.autopush`,
  `mailbox.dir`.
- The `server` and `remotes` sections are managed by `server` and `connect` commands only.
  `set` refuses them and names the command to use.
- Values are validated before writing: `lang` must be `en` or `es`, `store.autopush` must be
  `true` or `false`, `machine` must normalise to a non-empty label and is stored normalised.
- The printed output never shows a token. It shows `set` in its place.
- A running `mnemo serve` reads the file when it starts. A change reaches open chats when they
  are restarted, and `set` says so.

Example output:

```
$ mnemo config
machine          laptop1                 hostname
lang             es                      config file
store.dir        /home/me/.local/share/mnemo   default
store.remote     me@vps:mnemo.git        environment (MNEMO_REMOTE)
store.autopush   false                   default
mailbox.dir      /home/me/.local/state/mnemo/mailbox   default
mode             connected to vps via me@vps
config file      /home/me/.config/mnemo/config.toml
```
