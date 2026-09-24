# Sync

How the store travels between machines through a git hub, and how a push is kept from publishing
a secret. This is the same in local and server mode. On a server, it runs against the server's
store.

Merging two memories is reading, not pattern matching, so resolving a conflict stays with the
agent. The git mechanics do not.

## Pull

`mnemo_sync`, under the store lock.

1. **No `origin`:** the result says the store has no remote and there is nothing to sync with.
   It is not an error.
2. **A rebase already in progress:** the result lists the conflicted files with their content
   and says to finish the rebase first.
3. **No tracking branch:** the result says the remote is wired but the branch tracks nothing yet,
   and that the first push sets it up. Guessing a branch could merge two unrelated memories.
4. Otherwise it runs `git pull --rebase --autostash`.
   - The conflicted files come from `git diff --name-only --diff-filter=U`, each returned with its
     content, conflict markers included.
   - Success with no conflicts: the result says it pulled, with git's stdout or
     `Already up to date.`.
   - Anything else: the result carries the conflicts, whether a rebase is in progress, and git's
     stderr, else its stdout, else `The pull did not complete.`.

## Resolve one conflicted file

`mnemo_resolve_conflict(file, content)`, under the store lock.

- The path is relative to the store. After cleaning it must stay inside the store, or it is
  refused.
- Content with any line starting with `<<<<<<<`, `=======` or `>>>>>>>` is refused, because a
  half-merged memory committed as resolved is worse than a conflict. The message says to merge
  both sides and send the finished file.
- The content is written with a trailing newline added when missing, then `git add -- <file>`.
- The result lists the files still conflicted.

## Continue or abort the rebase

`mnemo_rebase(action)`, with `continue` or `abort`, under the store lock.

- With no rebase in progress, the result says so and nothing runs.
- `continue` while files are still conflicted is refused, and the result names them.
- Otherwise it runs `git rebase --continue` or `git rebase --abort`. The result carries git's
  output, or `Rebase aborted; the store is as it was.` or `Rebase continued.`.

## Push

`mnemo_push(acknowledge?)`, under the store lock. Every refusal below is a normal result, not an
error.

1. **No `origin`:** refused as `no-remote`. The store lives only on this machine.
2. **Rebase in progress:** refused as `rebase-in-progress`.
3. **The base** is what the hub already has: the upstream branch when it resolves, else
   `origin/main` when it resolves, else git's empty tree
   `4b825dc642cb6eb9a060e54bf8d69288fbee4904`, so that a first push scans the whole history.
4. **No commits:** refused as `nothing-to-push`, saying the store has no commits yet.
5. **Nothing new:** when `git rev-list --count <base>..HEAD` is 0, refused as `nothing-to-push`,
   saying everything is already on the hub.
6. **The secret scan** runs on what would be published. There is no path to the network that
   skips it.
   - With findings and no acknowledgement, the push is refused as `secrets`.
   - With findings and an acknowledgement that does not match, it is refused as
     `bad-acknowledgement`.
   - Either refusal carries the findings and the acknowledgement token.
7. **The push:** `git push -q` when there is an upstream, else
   `git push -q -u origin <branch, or main>`.
   - Failure: the result says `git push failed:` with git's output.
   - Success: `Pushed <n> commit(s) to the hub.`.

### Acknowledgement token

The first 12 hex characters of the SHA-256 of the HEAD commit id, a newline, and the findings'
`rule|file|line` lines, sorted and joined by newlines. It changes when the commits or the findings
change. It is never stored.

## Secret scan

**Only added lines are scanned.** With the hub's branch as the base, those are exactly what the
push would publish. Removed lines and context lines are already on the hub, and flagging them
would block every future push over an old leak.

**This is the second line, not the first.** The same rules run before a memory or a pending list is
written, as [store.md](store.md#rules-for-every-write) says, so a secret an agent was about to save
never reaches the disk. The scan here catches what that one cannot see: a file the user edited by
hand, and anything written before the rules existed.

### Rules

| Name | Pattern |
|---|---|
| private key block | `(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----` |
| AWS access key id | `\bAKIA[0-9A-Z]{16}\b` |
| GitHub token | `(?i)\b(?:ghp\|gho\|ghu\|ghs\|ghr)_[A-Za-z0-9]{20,}\b` |
| GitHub fine-grained token | `(?i)\bgithub_pat_[A-Za-z0-9_]{20,}\b` |
| Slack token | `(?i)\bxox[baprs]-[A-Za-z0-9-]{8,}` |
| URL with embedded credentials | `(?i)\b[a-z][a-z0-9+.-]*://[^/:@\s]+:[^/@\s]+@` |
| credential assignment | `(?i)\b(?:password\|passwd\|secret\|token\|api[-_]?key)["'\s]*[:=]["'\s]*([A-Za-z0-9+/_.-]{12,})` |

- The credential assignment rule needs an unbroken token of at least 12 characters. A looser rule
  fires on ordinary notes such as `token: rotate every 15 minutes`, and a false positive blocks
  syncing memory.
- All patterns are valid for Go's RE2 engine as written. The `\|` in the table is a `|` in the
  pattern.

### Reading the diff

The diff is `git diff --no-color --no-ext-diff <base> HEAD`. Its lines are read in order:

1. `+++ b/<path>` sets the current file.
2. `@@ -a[,b] +c[,d] @@` sets the current line number to `c`.
3. Other lines starting with `---` or `+++` are skipped.
4. A line starting with `+` is scanned without its `+`. Each rule reports its first match on the
   line. Then the line number advances.
5. A line starting with neither `-` nor `+` is context, and the line number advances.
6. A line starting with `-` changes nothing.

### Findings

Each finding has the rule name, the file, the line number in the file as it would be pushed, and
an excerpt.

- **The excerpt never contains the secret.** The match is replaced, once, by
  `[redacted <n> chars: <shown>]`. `<shown>` is the first three and last two characters with `…`
  between them when the match is longer than eight characters, else `…`. The excerpt is then
  trimmed and cut to 200 code points.
- A finding travels through an agent transcript, so echoing the secret would be its own leak.

## Autopush

`store.autopush` changes nothing in the tools. `mnemo_status` reports it:

- on: `autopush: on — the user has opted into pushing without being asked each time`;
- off: `autopush: off — confirm with the user before calling mnemo_push`.

The skills and prompts read that line to decide whether to ask. The secret scan and the conflict
rule apply either way.
