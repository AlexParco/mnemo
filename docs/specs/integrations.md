# Tool integrations

How mnemo is registered in Claude Code, Codex and opencode, how `mnemo run` opens an agent, and
what the Claude Code plugin ships. Each tool is one integration with the same four operations: add,
remove, check and run. Supporting another tool later means adding one more integration.

## Which `mnemo` the entries point at

Codex and opencode entries use an absolute path, because a tool launched from a desktop launcher
may not have `~/.local/bin` on its PATH.

- The path is the one found by looking up `mnemo` on the PATH, when it is the same file as the
  running executable. Otherwise it is the running executable's path.
- Symbolic links are not resolved. A package manager's versioned directory would break the path at
  the next upgrade.

The Claude Code plugin cannot know an absolute path, so it runs `mnemo` from the PATH. `mnemo mcp
add claude` warns when `mnemo` is not found there.

## `mnemo mcp add [claude|codex|opencode] [--path P]`

- **Without a tool name**, each of the three is added when its executable (`claude`, `codex` or
  `opencode`) is on the PATH, and skipped otherwise. The output has one line per tool:
  `added`, `already up to date`, `skipped (not found)`, or the failure.
- **With a tool name**, that tool is added even if its executable is not found, except Claude Code,
  whose plugin needs `claude` to install.
- **Adding is idempotent.** Running it again rewrites mnemo's entry with the current path and
  changes nothing else.
- **Every file is written atomically**, keeping the mode the file had and following a symlink rather
  than replacing it: a config fed from a dotfiles repository keeps being fed from it, and a `0600`
  file carrying provider keys does not become world-readable.
- **Before each change, a copy is saved** next to the path given, as `<file>.mnemo-backup`, and it is
  refreshed every time. The only thing it protects against is the edit mnemo is about to make; a
  snapshot kept from the first run would have lost every change the person made since.

### Codex

- **File:** `$CODEX_HOME/config.toml`, else `~/.codex/config.toml`, or the `--path` file.
- **Entry**, delimited by marker comments:

  ```toml
  # mnemo: begin — managed by `mnemo mcp add`; remove with `mnemo mcp remove codex`
  [mcp_servers.mnemo]
  command = "/home/me/.local/bin/mnemo"
  args = ["serve"]
  env = { MNEMO_TOOL = "codex" }
  env_vars = ["MNEMO_AGENT"]
  # mnemo: end
  ```

- **Editing is textual**, because TOML libraries drop comments when they rewrite a file. An existing
  marked block is replaced in place. Otherwise the block is appended at the end, after a blank line.
- **Markers are matched as whole lines.** A comment that quotes a marker, or a string value
  containing one, is not a marker: treating it as one would cut out everything between it and the
  real end marker.
- **A begin marker with no end marker** is reported as mnemo's own block having lost its end line,
  not as somebody else's entry. **Two begin markers** are refused rather than guessed at.
- **Removing is checked the same way adding is:** what would be left is parsed as TOML, and nothing
  is written if it does not parse.
- **An entry mnemo did not write** is a table `mcp_servers.mnemo` outside the markers. Adding
  refuses, says where it is, and asks the user to remove it first.
- **After editing**, the whole file is parsed as TOML. mnemo checks that `mcp_servers.mnemo.command`
  is the path it wrote. If either check fails, nothing is written.

### opencode

- **File:** `~/.config/opencode/opencode.json`, or the `--path` file.
- **Entry** under `mcp.mnemo`:

  ```json
  {
    "type": "local",
    "command": ["/home/me/.local/bin/mnemo", "serve"],
    "enabled": true,
    "environment": { "MNEMO_TOOL": "opencode" }
  }
  ```

- **A missing file** is created with `"$schema": "https://opencode.ai/config.json"` and the entry.
- **An existing file** is edited with a JSON path setter that keeps the rest of the file's
  formatting.
- **An entry mnemo did not write** is an `mcp.mnemo` whose `command` is not exactly a `mnemo`
  executable followed by `serve`. One carrying its own flags belongs to whoever wrote it. Adding
  refuses, and removing leaves it alone.
- **A file that is not plain JSON**, for instance one with comments, is left alone. Adding prints the
  entry for the user to paste.

### Claude Code

1. `claude` must be on the PATH. Otherwise the output is the two commands below for the user to run.
2. mnemo warns when `mnemo` is not on the PATH, because the plugin runs it from there.
3. It runs `claude plugin marketplace add AlexParco/mnemo`. A marketplace that is already added
   counts as success.
4. It runs `claude plugin install mnemo@mnemo`. A plugin that is already installed is updated
   instead.
5. On any failure it shows the command's output and prints the two commands.

**`--path` names one file, so it needs one tool named.** With no tool, each of the three would write
its own format into the same file and the next would refuse it.

**An `mcpServers.mnemo` mnemo did not write is refused**, in the file route as much as the others: an
`.mcp.json` is usually committed, so that entry is often a teammate's, with their own store.

**After installing, the plugin's version is checked** against the binary's. A marketplace is a name,
and a name can serve a different implementation than the one running; installing that and reporting
success would hand a chat a mnemo that keeps its memory somewhere else. A mismatch is refused, says
which version was installed, and gives the command to remove it.

**With `--path P`**, the plugin is not installed. The file P, shaped like `.mcp.json`, gets:

```json
{ "mcpServers": { "mnemo": { "command": "/home/me/.local/bin/mnemo", "args": ["serve"], "env": { "MNEMO_TOOL": "claude" } } } }
```

The output says that this gives the tools only, without the monitor or the `/mnemo:*` commands.

## `mnemo mcp remove [claude|codex|opencode] [--path P]`

- **Without a tool name**, it removes mnemo from all three.
- **Codex:** it removes the marked block.
- **opencode:** it removes `mcp.mnemo` when mnemo wrote it.
- **Claude Code:** `claude plugin uninstall mnemo@mnemo`, or, with `--path`, the `mcpServers.mnemo`
  key.
- **Nothing to remove** is reported, and the exit code is 0.

## Checking a registration

`mnemo run` checks the tool it opens. A tool is registered for a directory when one of these holds:

| Tool | Global | In the directory |
|---|---|---|
| Claude Code | `mnemo@mnemo` is installed and enabled | `.mcp.json` has `mcpServers.mnemo` |
| Codex | the global file has the marked block | `.codex/config.toml` has `mcp_servers.mnemo` |
| opencode | the global file has `mcp.mnemo` | `opencode.json` has `mcp.mnemo` |

## `mnemo run <tool> --name <name> [dir] [-- <tool arguments>]`

1. **The name** must match `^[a-z0-9][a-z0-9-]{0,62}$`.
2. **The machine label** must resolve.
3. **The directory** defaults to the current one and must exist.
4. **The registration** must pass the check above. Otherwise it stops:
   `mnemo is not registered in Codex. Run: mnemo mcp add codex`.
5. **The tool's executable** must be on the PATH.
6. **When connected**, the server must answer, bringing the tunnel up if needed. Otherwise it stops
   with the reason. A chat that cannot reach its memory is not opened.
7. **The address `<name>@<machine>`** must be free: in the local mailbox, or through the holder
   endpoint when connected. Otherwise it stops:
   `worker-api@laptop1 is already open in claude since 14:03. Close that chat, or pick another name.`
8. **It sets `MNEMO_AGENT=<name>`** in the environment.
9. **It opens the tool** in the directory with the arguments after `--`.
   - **Unix:** it changes directory and replaces its own process with the tool, so the terminal and
     signals go straight to the tool.
   - **Windows:** it starts the tool as a child with the same standard streams, waits, and exits
     with the tool's exit code.

Between step 7 and the tool starting, another chat could take the name. The claim made by the
chat's own `mnemo serve` is the one that counts, and the loser sees the identity warning in every
mailbox tool.

## Claude Code plugin

The plugin lives in the repository under `plugin/`. The marketplace catalogue is at the repository
root.

```
.claude-plugin/marketplace.json
plugin/.claude-plugin/plugin.json
plugin/monitors/monitors.json
plugin/hooks/hooks.json
plugin/skills/{forget,list-context,load-context,mem,rename,save-context}/SKILL.md
```

- **`marketplace.json`** names the marketplace `mnemo`, with one plugin `mnemo` whose source is
  `./plugin`.
- **`plugin.json`** carries the name, the version, the description, the author, the homepage, the
  repository, the licence and the keywords. Its MCP server is:

  ```json
  "mcpServers": { "mnemo": { "command": "mnemo", "args": ["serve"], "env": { "MNEMO_TOOL": "claude" } } }
  ```

- **`monitors.json`:**

  ```json
  [{ "name": "mailbox", "command": "mnemo watch --tool claude", "description": "mnemo mailbox: messages sent to this chat" }]
  ```

- **`hooks.json`:** a `PreToolUse` hook matching `Edit|Write` runs `mnemo hook suggest-save`. There
  is no `SessionStart` hook, because there is nothing to build.
- **The skills** are the six skills listed below. Each marked region `<!-- mnemo:rule NAME -->` must
  be identical to the rule text of the same name, and a test enforces it.
- **The plugin's version** equals the release version.
- **Hidden commands are a contract with the plugin.** `serve`, `watch --tool` and
  `hook suggest-save` keep their interface across releases, because a plugin and a binary of
  different versions can meet.

## `mnemo watch`

A hidden command, run by the plugin's monitor. Each line it prints becomes a notification, and a
notification wakes a chat that is idle at the prompt.

- **The name** comes from `MNEMO_AGENT`. Without it the command exits 0 at once, silently. A chat
  opened without `mnemo run` has no messages to announce.
- **The address** is the name and this machine's label. The tool comes from `--tool`, for group
  messages.
- **Every 2 seconds** it runs `peek` on the local mailbox, or asks `/v1/mailbox/unread` when
  connected, bringing the tunnel up if needed.
- **It announces each waiting message once** per run, starting with what is already waiting when it
  starts. It never marks anything read and never claims the name.
- **Line format:**

  ```
  [mnemo] message <id> from <from>[ in reply to <reply_to>]: "<preview>" Read it with mailbox_read.
  ```

  The preview is the body with runs of whitespace collapsed to one space and trimmed. Past 120 code
  points it is cut to 119 followed by `…`.
- **Errors** go to stderr, once per distinct error, and the watch continues. A tunnel that drops for
  a minute must not end it.
- **It exits** on SIGINT or SIGTERM, and when stdout is closed.

## `mnemo hook suggest-save`

A hidden command that ports the plugin's save reminder. It nudges toward `/mnemo:save-context` when
unsaved work piles up, and never blocks an edit.

- **Input:** the hook's JSON on stdin, with `session_id` and `transcript_path`. Without a session id
  it does nothing.
- **It does nothing** when the store directory does not exist.
- **Settings**, from the environment only:

  | Variable | Default | Meaning |
  |---|---|---|
  | `MNEMO_SAVE_EDITS` | 40 | unsaved edits before a nudge, and between nudges; 0 turns it off |
  | `MNEMO_SAVE_TOKENS` | 0 | context size in tokens that also nudges; 0 is off |
  | `MNEMO_SAVE_TOKENS_STEP` | 60000 | tokens between context nudges |

- **Language:** Spanish when the resolved `lang` is `es`, else English.
- **State per session:** `<state>/suggest-save/<session_id>.json` holds `edits`, `editsAtLastNudge`,
  `tokensAtLastNudge` and `storeHead`. Files older than 7 days are deleted.
- **A save resets the count.** The store's HEAD is read from the files in `.git` without running git.
  When it differs from the stored one, the unsaved count starts again.
- **Edit signal:** every call counts one edit. When the unsaved edits reach the threshold, it nudges
  and marks the point.
- **Context signal**, when enabled: the last usage entry in the transcript gives input tokens plus
  cache-read tokens plus cache-creation tokens. It nudges when that total is at least the threshold
  and at least one step above the last context nudge. The transcript format is Claude Code's own and
  may change, which is why this signal is off by default.
- **Output** when nudging, as JSON on stdout:
  `{"systemMessage": m, "hookSpecificOutput": {"hookEventName": "PreToolUse", "additionalContext": m}}`
  - English: `mnemo: <reason> this session. Consider /mnemo:save-context <project> so you don't lose progress.`
    The reason is `<n> unsaved edit` or `<n> unsaved edits`, or `context at ~<k>k tokens`.
  - Spanish: `mnemo: <reason> en esta sesión. Considera /mnemo:save-context <proyecto> para no perder el avance.`
    The reason is `<n> ediciones sin guardar`, or `contexto en ~<k>k tokens`.
- **It never fails.** Every error ends silently with exit code 0. A broken hook must not get in the
  way of work.
