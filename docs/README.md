# mnemo design

The design of mnemo. Everything here is decided first and the code follows it, so a spec that
disagrees with the code is a bug in one of the two.

## Reading order

| Document | What it covers |
|---|---|
| [cli.md](cli.md) | What mnemo is, the modes, every command, the happy paths |
| [architecture.md](architecture.md) | Packages, dependencies, how a call flows, libraries, build order |
| [specs/config.md](specs/config.md) | Config file, paths, setting resolution, machine label, `mnemo config` |
| [specs/store.md](specs/store.md) | Store format, parsing, writes, bootstrap, rename and forget, lock |
| [specs/card.md](specs/card.md) | The resume card: what it shows and how it is laid out |
| [specs/sync.md](specs/sync.md) | Pull, conflicts, push, secret scan |
| [specs/mailbox.md](specs/mailbox.md) | Addresses, holders, messages, delivery, retention, files |
| [specs/mcp.md](specs/mcp.md) | Instructions, every tool with its texts, prompts, rule texts |
| [specs/remote.md](specs/remote.md) | Server service, HTTP interface, pairing code, tunnel, relay |
| [specs/integrations.md](specs/integrations.md) | `mcp add`, `run`, the Claude Code plugin, `watch`, the save reminder |
| [specs/release.md](specs/release.md) | Versions, build, install, update, uninstall, migration |
| [specs/testing.md](specs/testing.md) | Test rules, harness, behaviours to cover, negative controls |
| [ideas.md](ideas.md) | What other memory systems do, what is worth taking, what was turned down |

## Sources

- **Every behaviour written here was verified**, in the code or by running it, not recalled.
- **Behaviours of other tools** that could not be verified here are listed as checks at the end of
  [cli.md](cli.md).

## Decisions taken while writing the specs

These are the calls the specs make beyond what [cli.md](cli.md) already settled. Each one is
explained where it appears.

1. **Settings** live in a TOML file, `~/.config/mnemo/config.toml`. Environment variables override
   it, and `mnemo config` reads and writes it.
2. **Machine labels are normalised** to lower-case letters, digits and dashes. Pending stamps are
   compared normalised too, so a stamp written before the rule existed still matches its machine.
3. **The card lists only what is left to do.** Plain text, no icons: the unchecked items of every
   section of `pending.md`, plus the memories of type `todo`, each showing where it came from and
   which machine it belongs to. Notes, finished work and services are asked for separately. The
   previous layout is not kept.
4. **Only named agents use the mailbox.** There is no registration call and no session addresses: a
   chat opened without `mnemo run` gets memory but no mailbox.
5. **A name is held** by a live process on the mailbox's machine, or by a 45-second lease renewed by
   a heartbeat from another machine. MCP sessions play no part.
6. **The server's MCP endpoint is stateless.** A connected chat's `mnemo serve` answers the handshake
   itself and forwards tool calls, so a server restart does not break open chats.
7. **A connected machine never falls back to its own files** when the server cannot be reached.
8. **A background process keeps the SSH tunnel**, and it is started on demand, so a reboot needs no
   command.
9. **Addressing:** a bare name must match exactly one known agent. A full address to an agent never
   seen is accepted with a warning.
10. **Replies:** several are allowed per message. The groups `@all` and `@<tool>` are kept.
11. **Limits:** a message body is at most 256 KiB, and an agent offline for 90 days with nothing
    unread leaves the peers list.
12. **A mailbox left by an earlier install is moved aside**, and the mailbox starts empty.
13. **Hidden commands:** `tunnel` and `hook suggest-save` exist but are not listed in help.
14. **Each tool's MCP entry sets `MNEMO_TOOL`.** Only `MNEMO_AGENT` comes from `mnemo run`.
15. **Tool configs:** Codex's file is edited as text between marker comments, and opencode's with a
    JSON setter that keeps formatting.
16. **The plugin lives in `plugin/`.** It has no build hook, and the save reminder is in the
    binary.
17. **Store hardening:**
    - files are written atomically;
    - every slug and id is validated before use;
    - forget does not report links from the files it deletes;
    - `MNEMO_AUTOPUSH=0` means off.
18. **The server listens on loopback only.** There is no option to listen elsewhere.
19. **`mnemo_guide` has no Cursor target.**
20. **The first release is `v0.5.0`.**
21. **The memory belongs to the person, not to the project.** One store outside every repository,
    shared between the user's machines, rather than notes committed inside a project and reviewed by
    a team. Written down in [cli.md](cli.md#whose-memory-this-is).
22. **A secret is refused when it is written**, not only when it would be published. The push scan
    stays as the second line, for files a person edited by hand.
23. **Every answer an agent gets is bounded and says where it stopped.** Loading a project returns a
    complete index with as many bodies as fit; a search that hit its limit says so.
24. **A fact that changed is superseded, not overwritten.** Both memories stay, the old one points
    at the new one, and neither is hidden from search.
25. **Retiring a whole store is a terminal command and never a tool.** It moves the store aside by
    default, refuses while work has not reached the hub, and never touches the hub itself.
