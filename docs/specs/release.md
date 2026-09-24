# Release, install and update

## Versions

- **Tags** are semantic versions, `vX.Y.Z`.
- **The first Go release is `v0.5.0`**, continuing the plugin's `0.4.0`. `1.0.0` waits until the Go
  version has been used across machines for a while.
- **The HTTP `api` number** is separate from the version. It changes only when the interface in
  [remote.md](remote.md) changes incompatibly. Server and clients must have the same `api`.
- **The plugin's version** in `plugin.json` equals the release version. The release process sets it.

## Build

- **GoReleaser**, with `CGO_ENABLED=0`.
- **Targets:** Linux, macOS and Windows, each on amd64 and arm64.
- **Build-time values:** version, commit and date.
- **Archives:** `mnemo_<version>_<os>_<arch>.tar.gz`, or `.zip` on Windows, each holding the binary,
  the licence and the README.
- **`checksums.txt`** with SHA-256 of every archive.
- **A GitHub Actions workflow** on each tag runs the full test suite, then GoReleaser, which
  publishes the release.

## Install scripts

Both scripts live at the repository root and are linked from the README.

### `install.sh`, for Linux and macOS

```
curl -fsSL https://raw.githubusercontent.com/AlexParco/mnemo/main/install.sh | sh
```

1. Detect the operating system and architecture from `uname`. An unsupported pair stops the script.
2. The version comes from `MNEMO_VERSION`, else the latest release from the GitHub API.
3. Download the archive and `checksums.txt`, and verify the archive with `sha256sum` or
   `shasum -a 256`. A mismatch stops the script, and nothing is installed.
4. Install the binary to `$MNEMO_INSTALL_DIR`, else `~/.local/bin/mnemo`, with mode `0755`. No sudo.
5. When that directory is not on the PATH, print the line to add to the shell's startup file.
6. Print the next step: `mnemo mcp add`.

### `install.ps1`, for Windows

```
irm https://raw.githubusercontent.com/AlexParco/mnemo/main/install.ps1 | iex
```

It follows the same steps with `Get-FileHash`. It installs to `%USERPROFILE%\.local\bin\mnemo.exe`,
and prints the command that adds that directory to the user's PATH when it is missing.

### Other channels

`go install github.com/AlexParco/mnemo/cmd/mnemo@latest` works from the first release. Homebrew,
Scoop and Winget come later, from the same GoReleaser configuration.

## `mnemo version`

```
mnemo 0.5.0 (commit 1a2b3c4, built 2026-09-20, api 1)
```

`mnemo --version` prints the same line.

## `mnemo update [--version vX.Y.Z]`

1. **Find the target:** the given version, else the latest release from
   `https://api.github.com/repos/AlexParco/mnemo/releases/latest`. When it is the running version,
   print `mnemo 0.5.0 is up to date` and stop.
2. **Refuse under a package manager.** When the executable is under a package manager's directory,
   meaning a Homebrew Cellar or a Scoop app directory, it says to update with that manager.
3. **Download** the archive and `checksums.txt`, and verify the archive. A mismatch stops, and
   nothing changes.
4. **Replace the executable atomically.**
   - **Unix:** write the new binary next to the old one, then rename it over.
   - **Windows:** rename the running `mnemo.exe` to `mnemo.exe.old`, then move the new one into
     place. The next mnemo command deletes `mnemo.exe.old`.
5. **Restart what runs in the background:** the server service when this machine has one, and the
   tunnel keeper when it is running.
6. **Update the plugin** when it is installed, with `claude plugin update mnemo@mnemo`.
7. **Print:** `Updated to 0.6.0. Open chats use the new version after they restart.`

## Uninstall

There is no uninstall command. The steps, in order:

1. `mnemo mcp remove`
2. `mnemo server remove`, on a server machine
3. `mnemo disconnect`, on a client machine
4. Delete the binary.

Data is never deleted by a command. What stays, for the user to keep or delete:

- the store;
- the state directory, which holds the mailbox, locks and logs;
- the config file.

## Coming from an earlier install

A store written by an earlier mnemo is read as is: the format and the default location do not
change, and there is nothing to convert.

1. **Stop the old one first.** Close every chat that uses it and stop any server it was running.
   Two different programs must never write to the same store at once; they do not share a lock.
2. **Hand-written MCP entries** under the name `mnemo`, in Codex or opencode, make `mnemo mcp add`
   refuse and name the entry. The user removes it, and `mcp add` writes the new one.
3. **A Claude Code project `.mcp.json`** pointing at an old HTTP server is removed by the user. The
   plugin replaces it.
4. **The mailbox starts empty.** Old messages are moved aside, as described in
   [mailbox.md](mailbox.md#statejson).
5. **Settings that are no longer read** are reported by `mnemo config` rather than ignored in
   silence: a server is set up with `mnemo server setup`, and a client with `mnemo connect`.
