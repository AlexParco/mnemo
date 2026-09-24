# Store

The store is the user's memory: plain text files in a git repository of its own. The format is
Markdown with YAML front matter, so every file stays readable, diffable and editable by hand
without mnemo.

Syncing with a hub is in [sync.md](sync.md). The resume card is in [card.md](card.md). Where
the store lives is in [config.md](config.md).

## Layout

```
<store>/
  .gitignore                  written with ".DS_Store\n" when missing
  projects/<slug>/INDEX.md    the project: identity, status, description
  projects/<slug>/pending.md  the project's living state
  memories/<id>.md            one fact per file, tagged with one or more projects
  shared/SCHEMA.md            the data contract, written from templates/SCHEMA.md when missing
  shared/<other files>        conventions that load with every project
```

- **A project exists when `projects/<slug>/INDEX.md` exists.** A directory without it is not
  a project for reads, overviews or writes.
- **The list of slugs** used in error messages is every directory under `projects/`, sorted
  by code point, with or without an `INDEX.md`.
- **Every slug and id received from a caller is validated before it touches the filesystem.**
  A value that is not kebab-case is reported as an unknown project or memory. Without this a
  value carrying `/` or `..` reaches a path, so the check is what keeps a tool argument from
  naming a file outside the store.

## Text rules

These rules are what the store format already assumes, and the format does not change.

- Files are UTF-8.
- **Line splitting** breaks on `\r\n`, `\n` and `\r`. A final line break does not produce an
  empty last line. Rarer break characters, such as the form feed, are not line breaks here.
- **Sorting** is by code point. Go's byte-wise comparison of valid UTF-8 gives the same order.
- **Truncation** to 100: trim; if the result has more than 100 code points, keep the first 99
  and append `…`.
- **Quote stripping** removes any run of `'` and `"` from both ends.

## Kebab-case

```
^[a-z0-9]+(?:-[a-z0-9]+)*$
```

Memory ids and project slugs must match it.

## Frontmatter

### Parsing

1. The text must start with `---`. Otherwise there is no frontmatter.
2. The block ends at the first `\n---` found at index 3 or later. If there is none, there is no
   frontmatter. A line such as `----` or `---x` also ends the block; that matches the original.
3. Each line of the block is trimmed and matched against
   `^([A-Za-z_][A-Za-z0-9_-]*):\s*(.*)$`. Lines that do not match are ignored.
4. The value is trimmed. If it has the form `[...]`, it is a list: split on `,`, trim each
   item, strip quotes, drop empty items. Otherwise it is a scalar with quotes stripped.
5. A key that appears twice keeps the last value.

- **Reading a key as a list:** missing gives an empty list, a scalar gives a one-element list.

### Round trip

A document is `fmText`, the text between the opening `---` and the closing `\n---`, and
`body`, everything after the closing marker. Rendering is `"---" + fmText + "\n---" + body`.
Parsing and rendering an untouched document gives back the same bytes, CRLF included.

### Editing one field

Writes that update an existing file change single fields and leave every other byte alone.

- A list renders as `[a, b]`. A scalar renders as it is, without quotes.
- With no frontmatter, `fmText` becomes `"\n" + key + ": " + value`.
- Otherwise the first line matching `^([ \t]*)<key>[ \t]*:[ \t]*.*$` is replaced by the same
  indentation plus `key: value`. If no line matches, `"\n" + key + ": " + value` is appended
  to `fmText`.

## Memories

- **File:** `memories/<id>.md`. The id is the file name without `.md`, and it is the
  authoritative id; the frontmatter `id` is expected to equal it.
- **Listing:** every name in `memories/` ending in `.md`, dotfiles included, sorted by code
  point. A file that cannot be read is skipped.
- **Fields:** `type`, `author` and `updated` are scalars, or empty when missing or a list.
  `projects`, `services` and `tags` are read as lists.
- **What `updated` means:** the day the file was last written. It is not the day the thing it
  describes happened, and it is not evidence that the fact still holds. A memory is a statement
  someone made on a date, and reading that date as the date of a decision is inventing history.
- **What is not recorded:** where the fact came from. `author` says who wrote the file, and that is
  all. If provenance is ever added, it splits in two, because the two are confused constantly: who
  or what produced the note — a person, an agent, a tool — and what it rests on — a commit, a file,
  a conversation, a link. One muddled field would be worse than none. Unknown frontmatter keys
  already survive every write, so this can be added later without a migration.
- **Summary:** the first line of the body that is non-empty after trimming, removing a leading
  run of the characters `#`, `-`, `*`, `>` and whitespace, Unicode spaces included, and trimming
  again. It is truncated to 100. A body with no such line has an empty summary.
- **Type order:** `decision`, `constraint`, `gotcha`, `bug`, `reference`, `todo`. An unknown
  type sorts after all of them.
- **Memories of a project:** the memories whose parsed `projects` list contains the slug
  exactly, stably sorted by type order, so file order holds within a type. The project filter
  never searches text. A slug that appears in a body, or inside a longer slug, is not a tag.

## Projects

- `name`: the frontmatter `name` when it is a non-empty scalar, else the slug.
- `status`: the frontmatter `status` when it is a non-empty scalar, else `?`.
- `services`: read as a list.
- `updated`: the scalar, or empty. As in a memory, it is the day the file was last written.
- `description`: the body, trimmed.
- `pending`: the parsed `pending.md`. A missing file is a valid state with no sections.

## Pending

- **Heading:** a trimmed line matching `^##\s+(.*)$`. The label is the captured text trimmed,
  and the key is the label in lower case. A heading whose key was already seen continues that
  section, and the section keeps the label it was first seen with.
- **Item:** an untrimmed line matching `^\s*-\s*\[( |x|X)\]\s*(.+)$` inside a section. Its
  text is truncated to 100, and it is done when the mark is `x` or `X`.
- Items before the first heading are dropped. Every other line is ignored.
- Sections keep the order in which they first appear.
- **Core sections:** in progress is `in progress` then `en curso`; next is `next` then
  `siguiente`. The open items of a group are the unchecked items of each key's section, in that
  key order.
- **Machine stamp:** the first `\[@([^\]]+)\]` in an item's text. The item belongs to another
  machine when the normalised stamp differs from the normalised label of the caller's machine.
  Normalisation is the one in [config.md](config.md#machine-label).

## Overview

What `mnemo_list_projects` reports.

- One entry per project with an `INDEX.md`: slug, name, status, services, updated, the number
  of memories tagged with it, and how many of those are tagged with more than one project.
- Sorted by status, `active` then `paused` then `done` then anything else, and then by
  `updated` descending in code-point order.
- Totals: projects; memories, counting every memory file; files in `shared/`, `SCHEMA.md`
  included; and orphan memories, which have a non-empty `projects` list that names no existing
  project.

## Search

- The query is lower-cased and split on whitespace into terms.
- `project` keeps memories tagged with that exact slug. `type` keeps memories of that type.
- Every term must be a substring of the lower-cased text made of the id, the type, the body,
  the tags joined by spaces, the services joined by spaces and the projects joined by spaces,
  separated by newlines.
- Results keep file order. `limit` defaults to 25 and must be from 1 to 200.

## Loading a project

Everything `mnemo_load_project` returns:

- the card, rendered for the caller's machine and language;
- the project;
- the memories of the project;
- every entry in `shared/`, sorted by code point, with its full content.

The store hands over all of it. What reaches an agent is bounded, because a project's memories grow
without limit and an answer that silently does not fit is worse than one that says so; the budget
and what it reports are in [mcp.md](mcp.md#mnemo_load_project).

## Writing

### Rules for every write

- Validation that does not need the store runs first, without the lock.
- Everything else runs under the store lock and starts with bootstrap.
- **Every file is written atomically**: a temporary file in the same directory, named
  `.<name>.mnemo-tmp-<random>`, then a rename over the target. A reader never sees half a
  file. Bootstrap removes temporary files older than one minute.
- Dates are the local calendar date as `YYYY-MM-DD`.
- A refusal is an error whose message tells the caller what to do instead.
- **A secret is refused before it is written.** Content bound for a memory or a pending list is
  scanned with the rules in [sync.md](sync.md#secret-scan), and a match stops the write. Doing it
  only at the push is too late: by then the value is on disk, in a commit, and in the agent's
  transcript. The push scan stays as the second line, because it also sees what a person edited by
  hand.

### Write a memory

Input: `id`, `projects` with at least one slug, `type`, `body`, and optionally `services`,
`tags`, `author`, `overwrite` and `supersedes`.

1. Without the lock, it refuses an id that is not kebab-case, a type outside the six, and a
   body that is blank after trimming.
2. It refuses when `projects` is empty or names a project that does not exist. The message names
   the missing slugs and the existing ones, and says to create the project with
   `mnemo_upsert_project` first, never silently.
3. It refuses when the memory exists and `overwrite` is not true. The message says to read it,
   merge, and write it again with `overwrite: true`.
4. A new memory is written exactly as:

   ```
   ---
   id: <id>
   projects: [<a>, <b>]
   services: [<…>]          only when non-empty
   tags: [<…>]              only when non-empty
   type: <type>
   author: <author>
   updated: <date>
   ---

   <body, trimmed>
   ```

   The default author is the store's `git config user.name`, else the operating-system user name.

5. An existing memory is updated field by field: `id`, `projects`, then `services` and `tags`
   when they are present in the call, even as empty lists, then `type`, then `author` when
   given, then `updated`. The body becomes `"\n\n" + body trimmed + "\n"`. Unknown keys and
   the original author survive.
6. For a new memory, the result lists related memories. Terms are the parts of the id that are
   at least four characters long. Each other memory scores the number of terms found in its
   lower-cased id and body. The result lists at most five with a score above zero, highest
   first, keeping file order on ties.
7. With `supersedes: <id>`, the memory it names is not deleted. It gets `superseded_by: <new id>`
   and a new `updated`, and both files stay. The old one must exist and must not be the new one.

### Superseding

When a fact changes, the honest record is both versions and the order between them, not one file
rewritten in place. Git has that history, but no agent reads git, so it is written where the agent
looks.

- **`superseded_by` is one more frontmatter key**, holding the id that replaced this memory. Unknown
  keys already survive every write, so nothing else changes and no migration is needed.
- **Nothing is filtered out.** A superseded memory is still found by search and still appears in a
  project's index, marked with what replaced it. Asking why something changed is exactly when the
  old version is wanted.
- **It is not a delete.** Deleting is `mnemo_forget`, which is for a memory that should never have
  been written. Superseding is for one that was right and stopped being right.
- **A chain is allowed**, and a cycle is not: writing a memory that supersedes something which
  already, directly or through other memories, supersedes it is refused.

### Replace a project's pending list

- The project must exist.
- The content is written as given, with a trailing newline added when missing.
- The result lists each parsed section with its label and its counts of open and done items.

### Create or update a project

Input: `slug`, and optionally `name`, `status` (`active`, `paused` or `done`), `services` and
`description`.

- The slug must be kebab-case.
- **Create**, when the project does not exist. `name` is required. It writes `INDEX.md` as
  exactly:

  ```
  ---
  slug: <slug>
  name: <name>
  status: <status, default active>
  services: [<…>]          always present, [] when none
  updated: <date>
  ---

  # <name>
  ```

  followed by `"\n" + description trimmed + "\n"` when the description is not empty. It writes
  `pending.md` as `# Pending — <name>\n`.

- **Update**, when it exists: `name`, `status` and `services` are set when given, and `updated`
  always. When `description` is present, even empty, the body becomes
  `"\n\n# " + name + "\n\n" + description trimmed + "\n"`, using the given name or else the
  existing one.

### Commit

1. Bootstrap.
2. Git needs a committer identity: `git config user.name` and `user.email` in the store. Without
   one it refuses and prints the two commands that set it for this store.
3. Lines matching `^\s*Co-Authored-By\s*:`, case-insensitive, are removed from the message.
   The rest is trimmed. An empty message is refused.
4. `git add -A`, then the staged names from `git diff --cached --name-only`. With nothing staged
   the result says nothing was committed. That is not an error.
5. `git commit -q -m <message>`.
6. The result gives the short sha, the files, how many trailers were removed, the unpushed count
   and whether the store has a remote.

An operation that already holds the lock, such as a rename, commits without taking it again.

## Bootstrap

Bootstrap creates what is missing and never overwrites. The order matters: on a second machine
pointed at a hub that already has memory, writing templates before adopting the hub's history
would leave untracked files in the way of the checkout.

1. Create `projects/`, `memories/` and `shared/`. The report says whether the store directory
   was created.
2. If the store is not a repository of its own, run `git init -q -b main`. A store is its own
   repository when the real path of `git rev-parse --show-toplevel` equals the store's real
   path. A store inside another repository gets its own, so its commits never land in the outer
   one. **This is a decision, not an accident.** A store placed inside a project would otherwise
   commit the user's memory into that project's history, and `add -A` would sweep up whatever the
   user had left uncommitted. It also means mnemo cannot be used the way a team wiki is, with notes
   committed alongside the code and reviewed in pull requests: that is a different product with a
   different owner. See [Whose memory this is](../cli.md#whose-memory-this-is).
3. If a hub remote is set and the store has no `origin`, add it. Then fetch from `origin`,
   tolerating failure. If `origin/main` exists and the store has no commits, run
   `git checkout -q -B main --track origin/main`. If that checkout fails, the store keeps its
   content and the report carries an explanation: the hub has memory, this store has local
   content, and the two were not merged.
4. Write `.gitignore` and `shared/SCHEMA.md` when missing.

The report lists: whether the store was created, whether the repository was initialised, the
remote wired in this call, whether history was adopted, the adoption explanation if any, the
templates written, and the committer identity or its absence. Bootstrap is idempotent.

## Git

- mnemo runs the system `git`. go-git cannot rebase and only fast-forwards on merge, and mnemo
  needs both.
- Commands that answer a question ignore stderr and treat failure as "no answer".
- Commands that act run with `GIT_EDITOR=true` and `GIT_TERMINAL_PROMPT=0`, so git never waits
  for an editor or a password. Their failure is an error carrying git's stderr.
- Commands that are expected to fail sometimes, such as a pull that hits a conflict, return both
  output streams and a success flag instead of an error.
- Every command runs as `git -C <store> …`.

### Status

- `isRepo`: the store is a repository of its own.
- `remote`: the URL of `origin`, or none.
- `branch`: `git symbolic-ref --short HEAD`, else `git rev-parse --abbrev-ref HEAD`.
- `dirty`: `git status --porcelain` is not empty.
- `unpushed`: `git rev-list --count @{u}..HEAD`, else the same against `origin/main`, else
  unknown.
- `rebaseInProgress`: the git path `rebase-merge` or `rebase-apply` exists. The path from
  `git rev-parse --git-path` is joined to the store unless it is absolute.

## Rename and forget

Both operations can lose memory, so both work in two calls. The first call returns a plan and a
confirmation token and changes nothing. The second call carries the token. It recomputes the
plan and acts only if the token still matches.

- **The token** is the first 12 hex characters of the SHA-256 of the plan's canonical JSON
  without the token. Canonical JSON uses the field order given below and no spaces. Tokens are
  never stored, so they survive a restart, they cannot be replayed against different content,
  and a plan that was applied is no longer the same plan.
- **Git state is not part of the plan.** A push between the two calls does not invalidate it.
- **Both need a clean worktree**, checked after bootstrap. Otherwise they refuse and say to
  commit first with `mnemo_commit`, because the operation makes its own commit.
- A token that does not match is refused with a message saying that nothing was renamed or
  deleted, and that the plan must be run again and shown to the user.

### Rename a project

Plan fields, in order: `kind` (`rename`), `from`, `to`, `memories`.

- `to` must be kebab-case and differ from `from`.
- `from` must exist. The refusal lists the existing slugs.
- `projects/<to>/` must not exist, because that would be a merge.
- `memories` lists the ids tagged with `from`, in file order.

Applying:

1. Rename the directory.
2. In `INDEX.md`, set `slug` and `updated`.
3. In each planned memory, replace `from` with `to` inside `projects`, keeping order and the
   other slugs, and set `updated`.
4. Check that no memory is still tagged with `from`, that `projects/<from>/` is gone and that
   `projects/<to>/INDEX.md` exists. A failed check is an error that says it is a bug.
5. Commit with the message `rename(<from> → <to>): <n> memories retagged`.

### Forget a project

Plan fields, in order: `kind` (`forget-project`), `slug`, `name`, `exclusive`, `shared`,
`brokenLinks`.

- The project must exist.
- `exclusive` lists the memories tagged with this project only. They will be deleted.
- `shared` lists the memories tagged with other projects too, each with the projects it keeps.
  They are untagged and never deleted.
- `brokenLinks` lists the files that contain `[[<id>]]` for a deleted memory, as store-relative
  paths sorted by code point. It searches the memories that survive and every other project's
  `INDEX.md` and `pending.md`. The deleted project's own files are left out: they are going
  away in the same operation, so a dangling link inside them is nobody's problem.

Applying:

1. Untag the shared memories first, setting `projects` to what remains and `updated`. If
   anything fails later, the side that must survive is already safe.
2. Delete the exclusive memories.
3. Delete `projects/<slug>/`.
4. Check that no memory is tagged with the slug, that the directory is gone, and that every
   shared memory exists with exactly its remaining projects.
5. Commit with the message `forget(project <slug>): <n> deleted, <m> untagged`.

### Forget a memory

Plan fields, in order: `kind` (`forget-memory`), `id`, `projects`, `summary`, `brokenLinks`.

- The memory must exist. The refusal says to search for it before assuming it is gone.
- `brokenLinks` searches every other file as above.

Applying deletes the file and commits with the message `forget(memory <id>)`.

## Retiring a store

Removing one project is `mnemo_forget`. Removing the whole store is a different operation, with a
different caller: it is reached only from the terminal, never as a tool, for the reason given in
[mcp.md](mcp.md#tools). What the user sees is in [cli.md](../cli.md#retiring-a-store); what happens
is here.

**The plan**, computed under the lock and changing nothing:

- the store's path, and whether it exists at all;
- every project with the number of memories tagged with it, and the totals: projects, memories,
  files in `shared/`;
- the hub's URL when there is one, and how many commits have not reached it;
- whether the worktree is dirty, which is unpushed work that is not even committed.

**Applying** needs the plan's confirmation and refuses without it, the same rule as rename and
forget: the token is a digest of the plan, so a store that changed since it was shown stops the
operation.

1. **Unpushed work refuses**, unless it is forced. Commits the hub has not seen, or a dirty
   worktree, mean this copy is the only one.
2. **The store is moved** to `<store>.retired-<date>`, in the same parent directory so the rename is
   atomic and never crosses a filesystem. With `purge`, it is deleted outright instead.
3. **A name that is taken** gets a counter: `<store>.retired-<date>.2`.
4. **The lock file for that store is removed** from the state directory, after the lock is released.
   Nothing else in the state directory is touched: the mailbox belongs to the machine, not to the
   store.
5. **The hub is never contacted.** No push, no fetch, no delete. What is on it stays on it.
6. **The config is left alone.** `store.remote` and the rest still point where they pointed, so the
   next save creates a fresh store and, if a hub is set, adopts what is on it.

The result says what was done, where the retired copy went when it was moved, and what the hub still
has.

## Lock

Several agents can write to one store at once, so every write takes a lock first.

- **One lock per store.** The file is `<state>/locks/<key>.lock`. The key is the store's real
  path, or its absolute path when it does not exist yet, with every run of characters outside
  `A-Z`, `a-z` and `0-9` replaced by `_` and leading and trailing `_` removed. The mailbox uses
  the same rule with its own directory.
- **Across processes** it is an operating-system file lock, taken with `gofrs/flock`. The
  operating system releases it when the process dies, so there is no stale-lock detection.
- **Inside one process** a one-slot gate per key is taken before the file lock, so the goroutines of
  one process queue for it in turn. The HTTP server runs many requests in one process. Where file
  locks belong to a process rather than to an open file, the gate is also the only thing keeping a
  process out of its own lock: on Linux and macOS the file lock already excludes a second
  descriptor, on AIX and Solaris it does not.
- **Nothing detects a caller deadlocking itself.** A nested acquire is indistinguishable from a
  second goroutine waiting its turn, so it fails by running out of time like any other wait.
- **Waiting:** retry every 50 ms for up to 10 s. A timeout is an error naming the lock file.
- **Running out of time is always reported as a timeout**, whoever holds the lock and whether the
  wait ended at the gate or at the file lock. A caller never sees a context error it did not set.
- **Not reentrant.** A public operation takes the lock once, and internal functions assume it is
  held. A nested acquire waits for itself and times out; a test proves that it fails loudly.
- **Reads do not lock.** Atomic writes are what keep them safe.
- **The lock file lives outside the store**, so it never shows in `git status`.
- **A different tool's lock is not this lock.** Anything else writing to the same store at the
  same time is outside what this guarantees. See
  [release.md](release.md#coming-from-an-earlier-install).
