# Testing

The test suite is the executable spec: the store, the card, sync and the mailbox are defined by what
these tests require of them, and this document is the list of those behaviours.

## Rules

- **Every test has a negative control.** A deliberate mutation of the code must make it fail. A test
  that still passes with the behaviour broken proves nothing. The controls are automated; see
  [Negative controls](#negative-controls).
- **Real processes over imports** wherever a boundary exists: the built binary, real MCP clients,
  real git, real HTTP. Unit tests cover parsers and algorithms.
- **Hermetic environment.** No test can reach the developer's store, hub, config, tools or SSH.
- **Race detector on.** The suite runs with `go test -race`.
- **Deterministic.** Clocks and random sources are injected where the behaviour depends on them:
  retention, leases, dates in files, tokens in ids.
- **Skipping is not passing.** A test that needs `git` fails in CI when it is missing, instead of
  skipping.

## Harness

`internal/testutil` provides:

- **`Env(t)`** creates temporary directories and sets `HOME`, `USERPROFILE`, `XDG_CONFIG_HOME`,
  `XDG_DATA_HOME` and `XDG_STATE_HOME` to them. It sets `MNEMO_CONFIG` to a temporary file and
  clears every other `MNEMO_*` variable. It sets `GIT_CONFIG_GLOBAL` to a temporary file with a test
  identity and `GIT_CONFIG_NOSYSTEM=1`. It puts a directory of stub executables first on the PATH.
- **The binary**, built once per package run with the race detector, for process tests.
- **Fixture stores** copied from `testdata/store/`. That fixture is an old store and is never
  modified, so every change is tested against data written before it.
- **Hubs:** bare repositories in temporary directories, and two or more clones acting as machines.
- **Stubs** for `claude`, `codex`, `opencode` and `ssh`. Each records its arguments, working
  directory and environment in a file. The `ssh` stub reads the `-L` specification and forwards TCP
  locally, so the tunnel keeper runs its real loop without a real SSH server.
- **MCP clients:** the SDK's client over the command transport for stdio, and over the Streamable
  HTTP transport for the server.

## What is tested

Each list below is behaviour, not a test-per-function list.

### Store

- The frontmatter round trip is byte-exact for every fixture file, including CRLF line endings and a
  body that contains a `---` rule.
- Frontmatter parsing: lists, scalars, quotes and empty items. A scalar `projects` reads as a list.
  A missing or unterminated block gives nothing.
- Editing one field rewrites only that line, keeps indentation, and appends a missing key.
- Pending parsing: unknown sections are kept in file order, only checkbox items count, items before
  a heading are dropped, a repeated heading accumulates, and core order is deterministic with both
  languages present.
- Machine flags mark only other machines, ignoring padding and case after normalisation.
- Project filtering: a longer slug is not a match, a mention in a body is not a tag, a scalar
  `projects` still matches, dotfile memories are included, and ordering is by type and then stable.
- Summaries strip leading markers, and an empty body has no summary.
- Overview order and counts, including orphans and projects with no memories or pending file.
- Search: case-insensitive, a spaced query matches a kebab-case id, every term must appear, filters
  compose, and the limit holds.
- Writing a memory gives exactly the schema shape, and optional fields appear only when given. It
  refuses what the schema forbids, and a project must exist first. An existing id needs overwrite,
  and overwriting keeps unknown keys and the original author. Near-duplicates are reported.
- Pending replacement reports what it parsed and refuses an unknown project.
- Project creation writes both files. An update touches only the given fields. Creating needs a name
  and a valid slug.
- Commit writes one commit per batch and reports that it is not on the hub. It strips
  `Co-Authored-By`, treats a clean store as no commit, and refuses without an identity, saying how to
  fix it.
- Bootstrap provisions a fresh store and is idempotent. The schema it writes is the embedded
  template. A store inside another repository gets its own. A remote is wired without adoption when
  the hub is empty. A hub never overwrites a store with content. An unreachable hub does not block a
  local store. A second machine adopts the hub's memory.
- Destructive operations:
  - the plan separates exclusive memories from shared ones;
  - applying keeps shared memories with exactly what remains;
  - untagging is surgical;
  - a slug in prose and a longer slug survive;
  - dangling links are reported, never repaired;
  - each operation makes its own named commit;
  - unknown targets are refused with the real slugs;
  - a change after planning invalidates the token;
  - a wrong token deletes nothing, and a second apply cannot happen;
  - uncommitted work is never folded in;
  - rename retags keeping overlap and order, leaves prose alone and ignores longer slugs;
  - rename refuses a merge, an invalid slug and a no-op.
- Lock: a second holder waits and times out; releasing lets a waiter in, and releasing twice is
  harmless; two directories do not block each other; a nested acquire times out instead of waiting
  for itself forever; the lock file is outside the directory it guards, and two names for one
  directory share it.
- Lock, across processes: a second process is kept out while this one holds it, and gets in once it
  is released. Running out of time against that process is reported as a lock timeout naming the
  file, never as a context error the caller did not set.
- Concurrency: many processes writing to one store at once all land, and the tree is left clean.

### Config

- The defaults with nothing set, every path in the table; asking where the store is does not create
  it. The XDG variables move all of them, and a relative XDG value is ignored and reported.
- A flag beats the environment, which beats the file, which beats the default, for every setting,
  and each one says where its value came from. A variable set to blank counts as unset.
- A path beginning with `~` means the home directory; `~user` is left alone.
- A relative path is pinned to the working directory when it is read.
- `MNEMO_AUTOPUSH` is on for anything but `0`, `false` and `no`. `autopush = false` written in the
  file is a decision and is reported as one; a file that never mentions it reports the default, and
  a save does not invent the key.
- The machine label is normalised, and a label that normalises to nothing says what to type.
- A missing file is not an error. Broken TOML names the file.
- A rewrite keeps every key mnemo does not understand, including fields inside a `[[remotes]]`
  entry, and does not turn mnemo's own keys into unknown ones.
- The file is written `0600` in a `0700` directory, the token survives it, no temporary file is
  left behind, and the destination is replaced by a rename — proved with a hard link, because
  nothing else tells a rename from a write in place after the fact.
- The agent name and the tool come only from the environment and never reach the file.

### Store, concurrency and safety

- Atomic writes: a reader polling during writes never sees a partial file, and stale temporary files
  are cleaned.
- Slug and id validation rejects path traversal in every tool that takes one.
- Broken-link reports exclude the files of the project being deleted.
- Goroutines inside one process serialise through the in-process mutex and the file lock together.
- A reader polling a file while it is rewritten always sees one whole version, never a mixture.
- Saving concurrently does not collide in git: every memory reaches the history and the tree is
  clean afterwards.
- A write carrying a secret is refused, and nothing reaches the disk: the memory file does not
  exist afterwards, and neither does a temporary file. The refusal names the rule and does not
  repeat the value.
- A pending list carrying a secret is refused the same way, and the previous list is untouched.
- Superseding keeps both memories: the old one gains the new one's id and a new date, the new one
  is written, and a search still finds both. A chain of three reads in order, and a memory that
  would supersede something that already supersedes it is refused.
- Superseding something that does not exist is refused, and writes nothing.
- Retiring a store: the plan counts what is there; a confirmation from a plan that no longer matches
  is refused; unpushed commits and a dirty worktree refuse unless forced; the default moves the
  store aside and the copy holds every file; a name already taken gets a counter; purging leaves
  nothing; the lock file goes and the mailbox stays; the hub is never contacted, proved by a bare
  repository that is byte-identical afterwards; the config still points at the same remote, and the
  next save builds a fresh store.

### Rule texts

- Every text is there and none is empty, and a text that ships with a `{{NAME}}` left in it fails.
- `CONFIRMATION_RULE` contains `CONFIRM_NOTE` word for word, and the guide contains each rule word
  for word, with all seven of its headings.
- Inclusion nests, an unknown name is refused, and a cycle is reported instead of recursing until
  the program dies.
- The bindings and the files must match, proved against a `rules/` that is wrong on purpose: a file
  nothing is bound to, and a binding with no file. Both stop the program at startup.
- Every marked region of every `.md` file of every skill is byte-identical to the text it names.
  Which skill carries which rule is a table, not a global set: two skills quote `SAVE_CRITERION`,
  and either copy could rot while the other kept the check green. A skill missing from the table,
  or a rule a skill quotes that the table does not expect, is reported.
- A region that cannot be read is reported rather than skipped. A trailing space after the marker,
  a CRLF line ending or a misspelt closing marker would otherwise take a wrong copy out of the
  comparison in silence, which is worse than the wrong copy.
- A skill directory that is a symlink is still walked.
- No text contains an emoji.
- The instructions stay under 1400 bytes and still name `mnemo_load_project`, `mnemo_push` and the
  `mailbox_*` tools.

### MCP, the read-only tools

- The card is the first block of a load, and carries no JSON. `detail: card` returns the card and
  the machine block, and nothing else.
- Every unchecked pending item comes back; ticked ones are capped at the 20 most recent, and a
  block says how many were left out and that nothing was deleted.
- An unknown slug is refused and names the real slugs. A missing store is a different refusal from
  an empty one.
- An empty search tells the agent to say so rather than fill the gap; a search that hit its limit
  says it was cut.
- Reading a memory returns the raw file and its fields; a missing one says to search first.
- Status without a slug does not carry a project; with one it reports the counts, what belongs to
  another machine, how many are ticked, and what comes next.
- The machine block carries the machine rule, what belongs elsewhere, and the warning not to read
  the card as a history.
- JSON is indented by two spaces and does not escape `<`, `>` or `&`.
- Every tool has a name, a description, a handler and a closed object schema whose every property
  is described and whose required fields exist.

### The binary over stdio

Against the built binary, started as a tool starts it, with a real MCP client over a pipe. Calling
the command in-process would skip the part most likely to be wrong.

- It offers the memory tools, and loading a project returns the card as its first block, carrying
  what is left and not what is finished.
- The machine label reaches the answer, which is what decides whether a stamped pending item is
  this machine's to act on.
- Starting the server does not create a store: a machine that has never saved is told so, and the
  directory is still absent afterwards.
- Nothing but the protocol reaches stdout. Proved with a setting mnemo cannot honour: the warning
  goes to stderr and the session survives it.
- `mnemo version` prints the version the package carries.

### MCP, the write tools

- Bootstrap says what it created, tells a store with no hub how to get one, and calling it again
  says there was nothing to do. A machine with no git identity is warned then, not at the first
  commit weeks later.
- Creating a project explains its empty pending list; updating one touches only the fields given.
  Both say the work is not committed.
- Writing a memory reports where it went and that it is not committed. An existing id needs
  `overwrite` and the refusal leaves the old body alone. A project that does not exist is refused
  by name and nothing is written.
- A secret is refused before anything reaches the disk, for a memory and for a pending list alike,
  and the refusal never repeats the value.
- Replacing a pending list hands back what mnemo parsed: every section with its open and done
  counts, because the file was replaced wholesale.
- A commit names the sha and the files, says a store with no remote lives only on this machine,
  reports a stripped `Co-Authored-By` trailer, and a second commit with nothing to do says so.
- A connection that may read but not write refuses every write tool by saying so, rather than
  crashing on a store that is not there.
- The whole bootstrap report, including the half that only happens on a machine with a hub: the
  hub is named, a store that has one is not advised to set one, and a second machine is told it
  adopted what was already there.
- A write carries every argument it was given: services, tags and author reach the file, overwrite
  really replaces and reports "Updated", and a near-duplicate is named.
- A commit on a machine with a hub says the work has not left it, and names the files.
- The write tools' schemas are enforced too: a missing required argument, a type or a status
  outside its enum, and a memory belonging to no project are all refused before a handler runs.
- End to end through the binary: a whole session — create, write, replace pending, commit — leaves
  the files a later session reads, and the next load shows the open item and not the finished one.

**A refusal and a failure are told apart by what they say**, not by the error flag, which both set.
A helper that only checked the flag would let the whole classification be deleted with every test
still passing, which is how it was found.

### Refusing rather than guessing

- A machine with no home directory has nowhere to keep a store: `mnemo serve` refuses, says why,
  and the directory it was started in is still empty afterwards. Proved against the binary.
- A store with no path is refused by name, with what to set.
- A load carries `pending.md` itself, not only the parse of it: the prose and the full text of a
  long item are in the answer, because the tool that rewrites the file replaces the whole of it.
- The refusal on an existing memory names both ways out — overwrite for a note still true,
  supersede for a fact that changed — and superseding keeps both.
- Waiting too long for the lock is advice, not a failure: another agent is saving and the caller
  should try again.
- A failed answer accepts no further blocks, so a refusal is always exactly one.

### MCP, talking to the hub

Two machines that really collided, one hub, the same line of the same pending list changed on both.

- A sync with conflicts comes back as three blocks: the detail and the file names, the files
  themselves, and the conflict rule. The files travel with the answer because the agent has to
  merge them and one it fetched separately could already have moved.
- Content that still carries conflict markers is refused, and so is a path outside the store.
- Resolving the last file says to call `mnemo_rebase continue`; resolving an earlier one names what
  is still conflicted.
- Continuing too early is a refusal that names the unresolved file. Aborting puts this machine's
  own work back and leaves no markers behind.
- After the merge, a third machine adopting from the hub sees both sides. That is the whole point
  of the rule, and it is checked on the hub rather than locally.
- A push scans first: the refusal is three blocks, the findings name the file, no block repeats the
  value, a wrong acknowledgement is rejected and the right one lets it through.
- No hub is a refusal. Nothing to push is not: the work is already published.

### MCP, the two that can lose memory

- Nothing happens on the first call: the plan reports what would change, carries a confirmation
  value, and ends on the confirm note. The store is untouched afterwards, checked.
- A value that was never issued is refused. The same value cannot be used twice, because applying
  changes the plan it described.
- Renaming keeps overlap, leaves a longer slug that contains the shorter one alone, and says the
  other machines still have the old slug until it is pushed.
- Deleting a project deletes only what was tagged with it alone, and the surviving memories are
  **named**, not counted: a number does not let anyone check the one promise that makes the
  operation safe.
- Links left pointing at a deleted memory are reported and not repaired, and the file that holds
  one still holds it afterwards.
- Deleting a memory does not take its project with it, and an unknown target names the real ones.

### Half a merge

The state a sync leaves behind, and the one where a wrong move loses a session.

- Nothing writes while the store is mid-rebase. Git allows it and says nothing, the commit is
  folded into the rebase, and aborting then destroys it without stashing it — so the whole of a
  session's memory can go because an agent did the two things it was told to do in the wrong order.
- Nothing that returns memory is read either: the files carry conflict markers, and the card an
  agent is told to print verbatim would show a user half a merge as fact.
- `mnemo_status` still answers, and names what to call: it is how a fresh session finds out at all.
- Aborting says what it cost — the hub's memory is still unmerged and the same conflict returns —
  and the store is writable again afterwards.
- Resolving is only for the files a sync left conflicted. Any other path is refused, and a
  committed memory is byte-identical afterwards.
- A merge is not refused for a flagged value that was already in the conflicted file: that value
  came from a commit on one side, so refusing would make the only correct merge impossible and the
  machine could never pull again. A value the merge adds is still refused, and the refusal tells the
  agent not to delete the line to get past it.
- A hub that is gone is a refusal, for a pull and for a push: reported as success the agent tells
  the user their memory is somewhere it is not.
- A file `.gitattributes` marks undiffable is still scanned for secrets.

### MCP, the server

- A client sees every defined tool, with the same description, and each one's annotations match
  the tool table of [mcp.md](mcp.md#tools) rather than matching themselves. The count offered
  equals the count defined.
- The instructions reach the client on the handshake, byte for byte, with the server's name and
  version. They are the only channel that reaches a client supporting neither prompts nor
  resources.
- Arguments are validated against the schema before any handler runs: a value outside an enum, a
  number outside its bounds and an argument the schema does not name are all refused.
- The order of the blocks survives the protocol.
- A refusal arrives as a result with `isError`, not as a protocol error the client reports as a
  broken server.
- A handler that panics is one refusal reading `mnemo failed unexpectedly: ...`, not a dead server.

### Registering mnemo in a tool

Every one of these is a way to damage a file mnemo did not write.

- Codex keeps its comments, its own settings and its trailing comments; the edit is textual for
  exactly that reason, and what is written parses as TOML and says what it should before anything
  reaches the disk.
- **Markers are whole lines, and a block only counts as mnemo's when it contains mnemo's entry.** A
  comment quoting a marker, and marker lines inside a multi-line string, are both left alone —
  matching them as substrings used to cut out everything between them and the real end marker, which
  is most of somebody's configuration.
- A begin marker with no end marker is reported as mnemo's own block having lost its end line, not
  as an entry mnemo did not write. Two begin markers are refused rather than guessed at.
- The mode of the file is kept and a symlink is followed rather than replaced: a config fed from a
  dotfiles repository keeps being fed from it, and a `0600` file carrying provider keys does not
  become world-readable.
- The copy kept beside a config is refreshed before each change, not kept from the first run: the
  only thing it protects against is the edit mnemo is about to make.
- An `mcpServers.mnemo` in a committed `.mcp.json` that mnemo did not write is refused and never
  deleted — it is usually a teammate's. An `mcp.mnemo` carrying its own flags is somebody's own.
- After installing the plugin, its version is checked against the binary's: a marketplace is a name,
  and a name can serve a different implementation.
- Adding twice says nothing changed. A binary that moved rewrites the one entry and does not
  duplicate it. Removing leaves the person's own settings and says nothing to remove the second
  time.
- An entry mnemo did not write is refused, named, and left byte-identical — for Codex and for
  opencode, adding and removing alike.
- An opencode file that is not plain JSON is left alone, and the refusal shows the entry to paste.
  A missing file is created with its schema.
- The copy kept before the first change is of the file as the person wrote it, and a later run does
  not replace it with mnemo's own output: the run that needs it is usually the one after the one
  that made it.
- Every file is replaced by a rename, proved with a hard link.
- The `.mcp.json` route says what it does not give: no mailbox monitor, no `/mnemo:*` commands.
- A plugin install that fails hands back what the tool said and the two commands to run by hand.
  A marketplace that is already added counts as success.

### Settings, from the terminal

- Usage errors have their own exit code, so a script can tell a mistyped command from one that ran
  and failed. A refusal is not a usage error.
- `mnemo mcp status` says what is registered, where the store is and whether it exists yet, and
  whether anything leaves this machine.
- Skipping every tool says why, and says a tool can be named to register it anyway.
- With no home directory, registering refuses rather than writing into the current directory, and
  the directory is still empty afterwards.
- Unsetting a key that was never set says nothing changed.
- A machine label that normalises to nothing is warned about, with what to type.
- `mnemo config` names where every value came from, prints the token as `set` and never as itself,
  and names the config file.
- `config set` validates before writing: a language mnemo cannot render, an autopush that is
  neither true nor false, and a machine label that normalises to nothing are all refused. A label
  is stored normalised. A section another command owns is refused by name.
- A key that is not a setting is refused and the answer lists the ones that are.
- `config unset` really removes the key, so the default applies again and the origin says so.
- `set` says the change does not reach a chat that is already open.

### Card

- The render equals its expected file, byte for byte, for every fixture project, in both languages,
  from a machine that matches the fixture stamps and one that does not.
- Expected files are written by hand from the spec, never regenerated from the render.
- A missing project is an error naming the slug and the store.
- Every rule of the layout has a fixture that exercises it: more tasks than the card shows, none at
  all, tasks bound to one other machine and to several, tasks from the project's own sections,
  tasks written inside notes, a project with no name, and a status outside the known set.
- A memory that is not a task never reaches the card, whatever its type.
- A ticked item never reaches the card.
- A stamp that differs from the machine only in case or separators is not shown as another machine's.

### Git, and the secret scan

- Asking git a question either answers or says it cannot. A store with no commits has an unborn
  branch, where `symbolic-ref` answers and `rev-parse HEAD` cannot, which is why the two are asked
  in that order.
- An action that fails carries git's own stderr, so nobody has to guess what it objected to. A
  command that is expected to fail sometimes hands back both streams instead.
- Git never waits for a person: no editor, no credential prompt.
- A directory inside another repository is not a store of its own, and neither is a directory with
  no repository. After an init it is.
- Status through the states of a store: fresh, dirty, committed, mid-rebase, wired to a hub, with a
  known count of what the hub has not seen and an unknown count when nothing tracks the branch.
- A missing committer identity is reported, half an identity counts as missing, and a commit
  without one fails.
- Staging one file and staging everything: what is staged is what the commit carries, and nothing
  is staged after it.
- Every secret rule has a sample, every sample is caught, and a finding locates the line without
  ever repeating what it found.
- A corpus of lines that must not be flagged, ordinary engineering prose included.
- Removed and context lines are not scanned. Line numbers point at the file as it would be pushed.
  A diff touching several files attributes each finding to its own.
- The scan starts from what the hub already has, so a leak that is already published does not block
  new pushes, and it starts from the empty tree when nothing has been pushed yet.
- The acknowledgement is bound to the commit and to the findings, and stops working when either
  changes.

Sample secrets are assembled at run time. A literal token in a committed file would trip push
protection on mnemo's own repository.

### Pull, conflicts and push

These run against a real hub: a bare repository and two clones of it, which is what two of the
user's machines are.

- A pull with no remote does nothing and does not fail. With a remote but nothing tracking the
  branch, it says the first push will set that up.
- A pull brings the other machine's work in, and says there is nothing new the second time.
- Two machines editing the same pending list: the pull reports the conflict with both sides and
  their markers, instead of guessing a winner, and says the store is mid-rebase.
- Resolving with the union of the two sides and continuing keeps both, and leaves the store clean.
- Content that still carries markers is refused, and so is a path that climbs out of the store.
- Continuing is refused while anything is unresolved, and names what. Aborting puts the file back
  exactly as it was.
- A push publishes once, sets the branch to track the hub, and afterwards says there is nothing
  left. It is refused with no remote, with no commits, and while a rebase is in progress.
- A secret stops the push, names the file, offers the value that would let it through, and leaves
  the hub untouched. A wrong value is refused. The one that was issued lets it through. The scan
  runs on a first push too, where there is no upstream to compare against.

### Mailbox

- Addresses: a bare name resolves when unique, is refused when unknown, and is refused with a list
  when ambiguous. An unknown full address is accepted with a warning.
- A message to an address waits for whoever holds it later.
- Two chats of the same tool on one machine are addressed separately.
- A held address is refused to a second instance: by claim, by heartbeat, and by `mnemo run` before
  launching.
- A local holder frees its name when its process exits. A remote holder frees it when its lease
  expires. A local idle holder whose process lives is never expired.
- A restarted chat resumes from its address's cursor.
- Groups reach everyone but the sender, reach by tool, and never reach addresses that appeared later.
- Replies go to the original sender with `reply_to`, only from a recipient, and several are allowed.
- Sending refuses an empty body, an oversized body, a message to yourself, an invalid group and a
  project that does not exist.
- Peers report live, offline and unread without inferring status from old data, and drop long-gone
  addresses.
- Retention: read messages expire after 30 days, unread messages to an address never do, and `seq` is
  never reused, even when the state file is lost.
- A torn last line does not corrupt the next message.
- Wait returns as soon as a message lands, times out empty, and stops when cancelled.
- `peek` shows what waits without marking it read or recording anyone.
- Version 1 files are moved aside, and the mailbox starts empty.
- A caller without a name gets the `mnemo run` refusal from every mailbox tool, and memory tools
  still work.
- Two real `mnemo serve` processes: a message crosses and the reply comes back; bursts from both
  lose nothing and share no `seq`; the tool shown comes from `MNEMO_TOOL`.

### MCP surface

- The whole tool list: names, descriptions, schemas and annotations. The local server and the relay
  list the same definitions.
- The instructions arrive in the handshake and say what the server cannot decide.
- All prompts are exposed and carry their rules.
- The guide names its file, the target changes only the wording, and it covers every rule the other
  channels carry.
- The copies cannot drift: `mnemo_load_project` returns the same machine rule as the guide, the
  destructive tools return the same confirm note, and the skills' marked regions equal the constants.
- Outputs: the card comes first and the rules last; `detail: card` drops the detail block; the
  language follows the request; an unknown slug fails and lists the real ones; search then read
  returns a memory; a missing memory is an error; status with and without a slug, and with an unknown
  slug.
- Bounded answers, against a project built to exceed them: the index lists every memory, the bodies
  stop at the budget, the entries past it are marked as such, and a block says how many came back
  without a body. Under the budget nothing is marked and no such block appears.
- A shared file larger than its cap comes back cut and says so.
- A search that returns nothing says that nothing is saved about it, in as many words. A search that
  hits its limit says there may be more; one that does not, does not say it.
- Loading a project whose pending list has ticked items says how many there are and that the card is
  not a history. With none ticked, that line is absent. The card itself is unchanged either way, and
  no ticked item ever appears on it.
- `mnemo_status` with a slug counts the ticked items separately from the open ones.
- A full save end to end over a real client: bootstrap, refusing a memory before its project,
  writes, one commit, the card reading it back, an empty second commit, and overwrite.
- Publishing end to end: a leaked credential stops the push over the wire, and the acknowledgement
  lets it through.
- Deleting end to end: reports without confirmation, errors on a wrong confirmation, and keeps the
  shared memory with the right one.

### Server and connection

- The server listens on loopback only, and refuses to start without a token.
- No token or a wrong one gets 401 before any work; malformed requests get the right status; unknown
  paths get 404.
- An authorised client gets the whole surface. Header identity names the caller. A missing machine
  header gets 400.
- A message crosses from a remote instance to a local stdio chat on the server, and the reply comes
  back.
- A restart of the server does not break a connected relay.
- Heartbeat claims and renews, a 409 comes back for a held name, and release frees it.
- Health reports the version and `api`, and a client with a different `api` refuses with the update
  message.
- Pairing codes round-trip, and a malformed code saves nothing.
- `connect` saves nothing when SSH fails, when the token is wrong, or when the machine labels are
  equal.
- The tunnel keeper runs one instance per machine, restarts SSH with backoff, exits when
  disconnected, and is started on demand by `serve`, `watch` and `run`.
- The relay forwards calls and results unchanged. It returns the not-sent and sent-without-answer
  refusals in the right cases, never writes to local files while connected, and releases its name
  on exit.
- Service files: the rendered systemd unit and plist match the spec exactly. Actually installing a
  service is part of the release checklist, not of the automated suite.

### Integrations

- `mcp add` for Codex writes the marked block, is idempotent, keeps comments and every other byte,
  refuses an entry it did not write, and validates before writing.
- `mcp add` for opencode creates the file, preserves formatting, refuses a foreign entry, and prints
  the entry when the file is not plain JSON.
- `mcp add` for Claude Code runs the plugin commands in order, prints them when `claude` is missing,
  and writes a `.mcp.json` entry with `--path`.
- `mcp remove` removes only what mnemo wrote.
- `run` checks everything in order and stops at the first failure with its message. It sets
  `MNEMO_AGENT`, opens the tool in the directory, and passes the arguments after `--`.
- The plugin: the manifest is valid, the monitor, hook and MCP server point at hidden commands that
  exist, and all six skills are present with bilingual trigger descriptions.
- `watch` announces each message once, leaves it unread, keeps going through errors without repeating
  them, and exits quietly without a name.
- `hook suggest-save`: silent when current, nudges at the threshold, resets after a save, prunes old
  state, and never fails.

### Config and release

- Resolution order for every setting. Normalisation of the machine label. File mode `0600`.
- `config set` validates, refuses managed sections, and never prints a token.
- `update` verifies checksums and changes nothing on a mismatch, against a local fake release server.

## Negative controls

- **Every control is a patch** in `testdata/mutations/<name>.patch`, with a header naming the tests
  that must fail.
- **`go run ./internal/testutil/mutate`** copies the repository to a temporary directory. For each
  patch it checks that the patch applies, so a patch whose anchor moved fails instead of silently
  doing nothing. It then runs the named tests and requires them to fail.
- **CI runs the controls** before every release, and on any change to a package they cover.

The minimum set. Each control names the behaviour it breaks.

**Written, and each one proved to fail its tests.**

- Let a package import one the table forbids, let the check allow every import, and let it skip a
  package it does not know: the dependency tests fail.
- Never take the file lock: the two tests that run a second process fail.
- Report a wait that ran out of time as a context error: the contention test fails.
- Parse memories by text search of the slug, count truncation in bytes, cut a string by bytes while
  counting code points, reverse the core section order, drop the one-element list a scalar stands
  for, lose a CRLF ending when rewriting a field, strip only ASCII whitespace from a summary, and
  drop the accents of a machine label: each fails the tests that cover it.
- Treat any directory inside a repository as a store of its own: the own-repo test fails.
- Scan removed lines too: the added-only test and the already-published-leak test fail.
- Put the line as written in the excerpt: the test that the finding never repeats a secret fails.
- Leave the commit out of the acknowledgement: it survives a new commit, and its test fails.
- Always scan from the empty tree: an old leak blocks new pushes, and its test fails.

**Deliberately absent.** Removing the in-process gate breaks nothing on Linux or macOS, where the
file lock already excludes a second descriptor. It is what keeps a process out of its own lock on
AIX and Solaris, and nothing here can show that, so no control pretends to.

**Also deliberately absent.** The TOML re-parse after removing mnemo's block from a Codex config has
no control. Once markers are matched as whole lines *and* a block only counts as mnemo's when it
contains `[mcp_servers.mnemo]`, every cut is between two whole tables of a file that already parsed,
so nothing reachable leaves invalid TOML behind. The check is kept as the one left standing if that
matching ever loosens again, which is exactly how the damage happened the first time.

**Also deliberately absent.** The symlink guard in `ResolveConflict` has no control. Nothing can
reach it: the check that the file is one a sync left conflicted comes first, and git does not report
a path through a symlink as conflicted. The guard is kept as the one left standing if that first
check ever changes shape, and a test asserts the refusal, but no mutation of it can fail.

**Also deliberately absent.** There is no control for the inverse of the refusal classification —
an internal fault reported as advice the agent could act on. It would need an error out of the
store that is neither a refusal nor reachable from a handler's arguments, and constructing one
means putting the store in a state it cannot be in.

**Also deliberately absent.** The walk over the plugin's skills lives entirely in the test: there is
no production code behind it, so the only way to break it is to edit the test, which is not a
control. Its two guards — every directory is resolved with `os.Stat`, so a skill shipped as a
symlink is still visited, and every opening marker the region pattern did not match is reported —
were each proved by hand against a skill made wrong on purpose, and both were green before the
guards existed.

- Write without scanning, adopt a hub with `--force` over local content, keep the co-author
  trailer, delete the memory a supersession replaces, allow a supersession chain to close on
  itself, write a file in place instead of atomically, and take no lock around a save: each fails
  the tests that cover it.
- Accept any confirmation when applying: the stale-plan, wrong-token and second-apply tests fail,
  for rename, for forget and for retiring a store.
- Leave git state out of the clean-tree check: the uncommitted-work test fails.
- Delete shared memories on forget: the overlap and surgical-untagging tests fail.
- Rename by replacing the old slug throughout the text instead of in the `projects` field: the
  test that prose survives fails.
- Retire while this copy is the only one: the tests for no hub, for unpushed commits and for
  uncommitted work fail.
- Push to the hub while retiring: the test that the bare repository is byte-identical afterwards
  fails.
- Change one word of a skill's copy of a rule text, leave an inclusion unresolved, accept a cycle,
  drop a text nothing is bound to, and let a missing text ship as an empty rule: each fails the
  tests that cover it.

**What the lock control taught.** Removing the lock did not fail anything at first: every write
lands atomically under its own name, so files alone do not need it. What needs it is git, which has
one index and one HEAD. The test was rewritten to save concurrently rather than only write, and the
control then failed every time, with git's own `index.lock: File exists`. A control that does not
fail is not a passing control; it is a test that was not testing.

**What the rename control taught.** Rewriting the whole text instead of the one field did not fail
anything at first. The decoys the test was built around are memories the rename never opens, so they
survive either way; the damage lands inside the two memories it does open, whose own prose nobody
was reading. The test now asserts that a retagged memory's body is exactly what the user wrote, and
the control fails.

**Waiting for the packages they test.**

- Do not persist the address cursor: the restart test fails.
- Ignore the group floor: the late-address group test fails.
- Accept any token: the 401 test fails.
- Ignore `X-Mnemo-Machine`: the header identity tests fail.
- Never expire leases: the remote holder test fails.
- Expire local holders by time: the idle local holder test fails.
- Fall back to local files when the server is unreachable: the relay test fails.
- Rewrite the Codex file through a TOML encoder: the comment preservation test fails.
- Resolve symlinks for the entry path: the path test fails.
