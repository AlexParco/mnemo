# Server and connection

How one machine's mnemo becomes reachable from others: the server, its HTTP interface, the pairing
code, the SSH tunnel and the relay a connected client runs.

## Roles

| Role | Runs | Uses |
|---|---|---|
| Local | `mnemo serve` over stdio, one per chat | this machine's files |
| Server | a service running `mnemo serve --http`, and `mnemo serve` over stdio for its own chats | its own files |
| Connected client | `mnemo serve` over stdio as a relay, and the tunnel keeper | the server's files, through the tunnel |

A machine is a connected client when its config has a remote with `connected = true`. It is a server
when its config has a `[server]` section. A server machine never connects.

## Server

### `mnemo server setup`

1. **It refuses on Windows**, and says that `mnemo serve --http` can still be run by hand.
2. **When a server is already set up**, it says so and whether the service is running. It does not
   print the code again, and it points to `mnemo server rotate` for a new one.
3. **The machine label** must resolve.
4. **The port** is 7433, or `--port`. It must be free on 127.0.0.1.
5. **The token** is 32 random bytes from the operating system's secure source, encoded as
   base64url without padding.
6. It writes `[server]` to the config.
7. It installs and starts the service for the platform, as described below.
8. It waits up to 10 seconds for the health endpoint to answer with the token.
9. It prints the pairing code once, and the next step for the other machine:
   `mnemo connect <code>`.

### Linux

- **The unit file** is `$XDG_CONFIG_HOME/systemd/user/mnemo.service`, else
  `~/.config/systemd/user/mnemo.service`:

  ```
  [Unit]
  Description=mnemo server
  After=network.target

  [Service]
  ExecStart=<absolute path to mnemo> serve --http
  Restart=on-failure
  RestartSec=2

  [Install]
  WantedBy=default.target
  ```

- **Setup** runs `systemctl --user daemon-reload`, then `systemctl --user enable --now mnemo.service`.
- **Lingering:** `loginctl show-user <user> --property=Linger` must say `Linger=yes`. When it does
  not, setup still installs, and it warns that the server stops when the user logs out, printing
  `sudo loginctl enable-linger <user>`. mnemo never runs sudo on Linux.
- **No user systemd:** when `systemctl --user` has no bus to talk to, setup stops and explains why.
- **Restart:** `systemctl --user restart mnemo.service`.
- **Remove:** `systemctl --user disable --now mnemo.service`, delete the unit, then
  `systemctl --user daemon-reload`.

### macOS

- **The plist** is `/Library/LaunchDaemons/io.github.alexparco.mnemo.plist`. It sets:
  - `Label`: `io.github.alexparco.mnemo`;
  - `UserName`: the user;
  - `ProgramArguments`: the absolute path to mnemo, `serve`, `--http`;
  - `EnvironmentVariables`: `HOME` set to the user's home, and a `PATH` of
    `/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin`, so git is found;
  - `RunAtLoad` and `KeepAlive` set to true;
  - `StandardOutPath` and `StandardErrorPath` set to `<state>/logs/server.log`.
- **Setup** writes the plist to a temporary file, then runs
  `sudo install -m 644 -o root -g wheel <tmp> <plist>` and `sudo launchctl bootstrap system <plist>`.
  sudo asks for the password in the terminal.
- **A LaunchAgent is not used.** It only runs while the user is logged in.
- **Restart:** `sudo launchctl kickstart -k system/io.github.alexparco.mnemo`.
- **Remove:** `sudo launchctl bootout system/io.github.alexparco.mnemo`, then delete the plist.

### Other server commands

- **`mnemo server restart`** restarts the service with the same token.
- **`mnemo server rotate`** generates a new token, writes it, restarts the service and prints the
  new code. Connected clients get a 401 until they run `mnemo connect <new code>`.
- **`mnemo server remove`** stops and removes the service and deletes the `[server]` section. The
  store and the mailbox stay.

### The service's environment

- The service does not have the user's shell environment. Its settings come from the config file.
- **Pushing to the hub** from the server needs git's SSH authentication to work without a
  terminal: a key without a passphrase, or an agent the service can reach. Setup checks this by
  running `git ls-remote origin` as the service would, and warns when it fails.

### `mnemo serve --http`

- Reads the port and the token from `[server]`, and refuses to start without them.
- Listens on `127.0.0.1:<port>` only. There is no option to listen anywhere else. Another machine
  reaches it through SSH.
- Logs to stderr: one line at start with the version, the address, the store and the mailbox, and
  one line per failed request.
- Exits cleanly on SIGINT and SIGTERM.

## HTTP interface

Every request needs `Authorization: Bearer <token>`. The check runs before the body is read, so an
unauthenticated request cannot touch the store or make the server buffer a body.

| Method and path | Purpose |
|---|---|
| `POST /mcp` | MCP over Streamable HTTP, stateless, with JSON responses |
| `GET /v1/health` | version check, used by connect, the relay and the tunnel keeper |
| `POST /v1/mailbox/heartbeat` | claim or renew a name for a remote instance |
| `POST /v1/mailbox/release` | give a name back |
| `GET /v1/mailbox/holder?address=` | whether an address is held, for `mnemo run` |
| `GET /v1/mailbox/unread?address=&after=` | what is waiting, for `mnemo watch` |

- **Token check:** SHA-256 of the presented token and of the real one, compared in constant time.
  Neither the token's length nor its content leaks through timing.
- **Errors:** no token or a wrong one gives `401` with `WWW-Authenticate: Bearer realm="mnemo"`. An
  unknown path gives `404`. A wrong method gives `405` with `Allow`. A body over 4 MiB gives `413`.
  Invalid JSON or parameters give `400`. Bodies are JSON: `{"error": "<message>"}`.
- **DNS rebinding:** the SDK's localhost protection stays on. The token covers the rest: a browser
  page aimed at 127.0.0.1 does not have it.

### `POST /mcp`

- The handler runs in stateless mode with JSON responses. There is no `Mcp-Session-Id`, so a server
  restart does not break a connected chat. That also follows the protocol's move toward sessionless
  operation. `GET` and `DELETE` give `405`.
- **Headers:**
  - `X-Mnemo-Machine` is required, and a missing one gives `400`.
  - `X-Mnemo-Tool` is required.
  - `X-Mnemo-Agent` is optional.
  - `X-Mnemo-Instance` is required whenever `X-Mnemo-Agent` is present.
- mnemo never sends requests from server to client, so stateless mode loses nothing it uses.
- **A cancelled `mailbox_wait`** may run on the server until its own timeout, at most 55 seconds.
  That is accepted.

### `GET /v1/health`

`200 {"version": "0.5.0", "api": 1, "machine": "vps"}`

- `api` is an integer. It changes only when this interface or the relay changes in a way the other
  side cannot handle.
- **A client refuses to work with a server whose `api` differs.** The error names both versions and
  says to run `mnemo update` on the older machine.

### `POST /v1/mailbox/heartbeat`

Request: `{"agent": "chat-a", "machine": "laptop1", "tool": "claude", "instance": "<16 hex>", "pid": 51234}`.

- `200 {"address": "chat-a@laptop1", "lease_seconds": 45}` when the name is claimed or renewed.
- `409 {"error": "<message>", "holder": {"tool": "claude", "since": "…"}}` when another live
  instance holds the name.

### `POST /v1/mailbox/release`

Request: `{"instance": "<16 hex>"}`. Returns `204`, even when that instance held nothing.

### `GET /v1/mailbox/holder`

Returns `200 {"held": false}`, or `200 {"held": true, "tool": "claude", "since": "…"}`.

### `GET /v1/mailbox/unread`

Returns `200 {"last_seq": 57, "messages": [ … ]}`, the result of `peek` in
[mailbox.md](mailbox.md#peekaddress-after). It records nobody and marks nothing read.

## Pairing code

- **Format:** `mnemo1_` followed by base64url, without padding, of
  `{"machine": "vps", "port": 7433, "token": "<token>"}`.
- **It contains the token**, so it is treated like a password. It is printed once, by `setup` and by
  `rotate`, and never logged or stored anywhere except in the client's config after `connect`.

## Connecting

### `mnemo connect <code>`

1. **Decode the code.** An invalid code is an error, and nothing is saved.
2. **The client's machine label must differ** from the server's.
3. **The SSH target** comes from `--ssh <target>`, else a prompt:
   `SSH connection to vps (user@host, or an alias from ~/.ssh/config):`. Without a terminal and
   without `--ssh`, it is an error.
4. **The local port** is 7433 when that port is free on 127.0.0.1, else a free port chosen by the
   operating system.
5. **A saved remote for another server** is replaced only after a yes, or with `--yes`.
6. **Start the tunnel keeper**, then wait up to 15 seconds for `/v1/health` through the tunnel.
   - When SSH fails, it stops, shows the last lines of SSH's error output, and saves nothing.
   - A `401` means the code does not match the server's token, perhaps because it was rotated.
     Nothing is saved.
   - A different `api` stops with the update message.
7. **Save the remote** with `connected = true`, and print:
   `Connected to vps through user@vps. Chats opened from now on use it; restart open chats to
   switch them.`

### `mnemo connect`

This form reuses what was saved: it sets `connected = true`, starts the keeper, checks health and
prints the same line. Without a saved remote it says to paste a code.

### `mnemo disconnect`

- Sets `connected = false` and stops the keeper.
- Prints: `Local mode. Chats opened from now on use this machine's files; restart open chats to
  switch them.`
- The saved remote is kept, so `mnemo connect` without a code returns to it.

### Local files while connected

- This machine's store and mailbox are left untouched while connected, and are used again after
  `disconnect`. Nothing is ever merged into the server.
- Memory from before connecting is shared through the git hub, as always.

## Tunnel keeper

`mnemo tunnel` is a hidden command that runs in the background and keeps the SSH forward open.

- **One per machine.** It holds an operating-system lock on `<state>/tunnel/keeper.lock`. A second
  keeper exits at once, successfully. It writes its process id to `<state>/tunnel/keeper.pid`.
- **Detached** from the terminal that started it. On Unix it gets a new session with its standard
  streams on the null device. On Windows it is started as a detached process in a new process group.
- **The loop:**
  1. Read the config. With no remote, or `connected = false`, exit.
  2. Run:

     ```
     ssh -N -o BatchMode=yes -o ExitOnForwardFailure=yes -o ServerAliveInterval=15
         -o ServerAliveCountMax=3 -L 127.0.0.1:<local_port>:127.0.0.1:<server_port> <ssh target>
     ```

  3. When SSH exits, wait and start again. The waits grow as 1, 2, 4, 8, 16 and 30 seconds, and
     stay at 30. A run that lasted at least 60 seconds resets them.
- **`BatchMode=yes`** means SSH never prompts. Key-based authentication is required, as the command
  reference says.
- **SSH** is the `ssh` on the PATH, or `ssh.exe` on Windows, which ships with the OpenSSH client.
  When it is missing, `connect` says so.
- **Logs** go to `<state>/logs/tunnel.log`. When the file passes 1 MiB it is renamed to
  `tunnel.log.1`, replacing any earlier one.
- **Stopping:** `disconnect` sets `connected = false` and sends the keeper a termination signal, or
  kills it on Windows. It waits up to 5 seconds. The keeper stops SSH before exiting.

### Bringing the tunnel up on demand

Every part that needs the server first checks `/v1/health` with a 2-second timeout. That covers
`serve`, `watch`, `run`, `peers`, `send`, `reply` and `read`. When the check fails, it starts the
keeper and checks again every 250 ms for up to 10 seconds. After a reboot, opening a chat is enough
to bring the connection back.

## Relay

In a connected client, the `mnemo serve` that a tool starts is a relay.

- **It answers locally** everything that does not touch data: `initialize`, the instructions, the
  tool list and the prompts. They are the same definitions as the server's, because the
  client refuses to work with a server whose `api` differs.
- **It forwards every tool call** to the server's `/mcp` through an MCP client session, and returns
  the server's result unchanged, including errors and structured content.
- **Headers** on every forwarded request: the token, `X-Mnemo-Machine`, `X-Mnemo-Tool`,
  `X-Mnemo-Instance`, and `X-Mnemo-Agent` when `MNEMO_AGENT` is set.
- **The client session** uses the Streamable HTTP client transport with its standalone event stream
  disabled. When a call fails at the transport level, the session is dropped and a new one is made
  on the next call.
- **Heartbeat:** when `MNEMO_AGENT` is set, the relay sends a heartbeat at start and every 15
  seconds. A `409` is kept and shown in the identity line; the server refuses mailbox calls for that
  name anyway.
- **Exit:** when stdin closes, it sends a release with a 1-second timeout and exits.
- **It never falls back to local files.** A client that silently wrote to its own mailbox would
  split the conversation without anyone noticing.

### When the server cannot be reached

A tool call that cannot reach the server returns a refusal. The wording depends on whether the
request was sent.

- **Not sent**, because the connection failed:
  `mnemo cannot reach the server on <machine> (<reason>). Nothing was done, here or there. The tunnel
  is being restarted; try again in a moment. If it keeps failing, tell the user to run mnemo connect.`
- **Sent, with no answer**, because of a timeout or a cut connection:
  `The connection to the server on <machine> dropped while it was handling this call (<reason>). It may
  or may not have taken effect. Check with mnemo_status or mailbox_peers before trying again.`

Each failure also triggers the on-demand tunnel check, at most once every 10 seconds.

## Terminal commands in connected mode

`mnemo peers`, `send`, `reply` and `read` use the same relay client. They set the tool to
`terminal`, the agent to `--name` and the instance to the command's own. They call the mailbox tools
and render the structured content for a person. Their output is in [../cli.md](../cli.md).

## Security summary

- The token lives only in the config file, with mode `0600`, and in the pairing code. It is never
  in a tool's config, in a process's arguments, in a unit file, or in a log.
- The server listens on loopback only, and the port is reached through SSH.
- Whoever holds the token is the user. Headers naming a machine or an agent are trusted, and are
  identification, not authorisation.
- Authorising an agent to act on a message is the user's decision, in the project's rules file.
