# mnemo

A shared memory and a mailbox for coding agents. Agents in Claude Code, Codex and opencode, on one
machine or several, load and save the same project context and send messages to each other.

**Status:** being rewritten in Go. Nothing is usable from this repository yet.

- [docs/](docs/) holds the design: commands, architecture and specs.
- [testdata/](testdata/) holds the fixture store, the expected cards and the mutation patches.
- [plugin/](plugin/) will hold the Claude Code plugin. For now it holds its skills.

```
go test ./...
```
