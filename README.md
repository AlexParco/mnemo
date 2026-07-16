# mnemo

Persistent, **per-project** memory for Claude Code: it stores your work context as **plain text**
in a git repo and keeps it **synced across your machines**. Simple, domain-agnostic, your data is
yours.

> **Status: early (WIP).** The commands and the hook run locally as a Claude Code plugin.
> The multi-machine flow is designed but lightly tested.

## Two separate pieces

| | What it is | Where it lives |
|---|---|---|
| **Plugin** (this repo) | The tool: the `/mnemo:*` slash-commands + the hook | GitHub — **shareable**. Anyone installs it and uses it for their own stuff |
| **Store** | *Your* memory: your projects and notes | Wherever you decide: a git repo on your server, or only on your machine |

Everyone installs the same plugin and has **their own private store**. Your context doesn't mix
with anyone else's.

**The plugin doesn't pick your server.** It only needs a git remote; what's on the other side is up
to you: a bare repo on a VPS, a private repo on GitHub/GitLab, a self-hosted Gitea. Or none, and
the store stays on this machine.

## How it works (in 30 seconds)

- A **note** = a `.md` in the store with tags at the top (`projects: [a, b]`, `type`, ...).
- Overlap is native: a note can belong to **several projects** at once (tags, not rigid folders).
  Loading a project = filtering the notes that include it.
- **`projects` vs `services`:** `projects` says *which project(s)* the note belongs to (overlap =
  several distinct projects); `services` distinguishes *internal parts* of a single project (repos,
  modules, areas). Two repos of the same product = **one** project with two `services`, not two
  projects.
- The commands do the dirty work: `pull --rebase` → read/write notes → `commit` → `push` (with
  your confirmation). You never write git by hand.

## Installation

**Requirements:** [Claude Code](https://claude.com/claude-code), `git`, `node` (the reminder hook)
and `python3` (the `load-context` card render). node and python3 usually come with the environment.

It's a Claude Code plugin. From Claude Code:

```
/plugin marketplace add AlexParco/mnemo
/plugin install mnemo@mnemo
```

Restart Claude Code (or run `/reload-plugins`) so it registers the commands. Verify with
`/mnemo:list-context` — the first time it will tell you there's no memory yet, and that's correct.

There's no installer or store setup step: **the store creates itself** the first time you run
`/mnemo:save-context <slug>` (git init + structure, in `~/.local/share/mnemo`). You don't need to
know git; the commands manage it for you.

> **Going to sync across machines?** Export `MNEMO_REMOTE` **before** the first
> `/mnemo:save-context` (see [Sync across machines](#sync-across-machines)). If you save first
> without a remote, the store stays local and you then have to hook it up by hand.

On a single machine this already works out-of-the-box, with no server or account for anything.

Variables: `MNEMO_DIR` changes the store path, `MNEMO_REMOTE` hooks up a remote at creation,
`MNEMO_AUTOPUSH=1` makes `save-context`/`mem` push on their own (see [Usage](#usage)),
`MNEMO_MACHINE` sets a nice label for this machine (default: the hostname) — see
[Machine-tied work](#machine-tied-work).

## Sync across machines

**The server doesn't run the plugin — it just stores your data.** The plugin runs on each
computer; the server is a git repo of yours (a bare repo) that acts as an always-on central point.
You pick the server; the plugin doesn't care as long as it speaks git.

```
       YOUR VPS                    EACH COMPUTER
  ┌──────────────────┐          ┌──────────────────────────┐
  │  mnemo.git        │◄────────►│  mnemo plugin (the tool) │
  │  (bare repo)      │  git     │  + local store            │
  │  = YOUR DATA      │  push/   │  ~/.local/share/mnemo     │
  └──────────────────┘  pull    └──────────────────────────┘
```

**1. On the VPS (once).** It only needs `git` and SSH access for you (with a key, ideally):

```bash
git init --bare ~/mnemo.git
```

**2. On your main machine.** Install the plugin (see above), and **before** the first
`/mnemo:save-context` export the remote in your shell rc:

```bash
export MNEMO_REMOTE=user@your-vps:mnemo.git   # path relative to the VPS home
```

The first `/mnemo:save-context <slug>` creates the store, hooks it to the remote and (with your
confirmation) pushes it. Your memory is now on the VPS.

**3. On each additional machine.** Install the plugin and export the **same** `MNEMO_REMOTE`. When
bootstrapping, instead of creating an empty store it **adopts the memory already on the VPS**. From
there on `/mnemo:load-context` brings in what's from the other machines and `/mnemo:save-context`
pushes yours.

If a store already existed without a remote, hook it up by hand once:

```bash
git -C ~/.local/share/mnemo remote add origin <URL> && git -C ~/.local/share/mnemo push -u origin main
```

If you prefer a private repo on GitHub/GitLab or a self-hosted Gitea, just change the URL and
nothing else. Mind the privacy: on a third-party service your memory lives on their disk.

### Conflicts

git resolves them, not a daemon. Before writing, the commands run `pull --rebase`; if two machines
touched the same note, the `.md` is merged keeping both sides. Since each memory is **one fact per
file**, clashes are rare; the only hot file is `pending.md`, where the correct merge is almost
always the union of the tasks. If the conflict is semantic (two decisions that contradict each
other), the command stops and asks you instead of choosing for you.

### Backup

The remote is your backup, on top of each machine's clone. If the server is yours, back it up like
you back up anything else of yours.

## Usage

| Command | What it does |
|---|---|
| `/mnemo:list-context` | overview of the store: projects, status, number of memories. Read-only |
| `/mnemo:load-context <slug>` | loads a project (INDEX + tagged notes + pending) and lets you resume. Slug required; without it → list |
| `/mnemo:save-context <slug>` | distills the session into tagged notes, updates pending, commits and (with your confirmation) pushes. Creates the store and the project if they don't exist. Slug required; without it → list |
| `/mnemo:mem <slug>[,slug2] <note>` | saves a loose note mid-session, local commit |
| `/mnemo:rename <old> <new>` | renames a project's slug (dir + INDEX + `projects` of each note, overlap-safe) |
| `/mnemo:forget project\|memory <x>` | deletes a project or a note (overlap-safe: a shared note is un-tagged, not deleted) |

**Push:** by default no command pushes anything without your confirmation (until you push, your
other machines don't see it). With `MNEMO_AUTOPUSH=1`, `save-context` and `mem` **push on their
own** — meant for a hands-off multi-machine flow. Even on auto, two guards are never skipped: a
**secret scan** stops the push if it detects keys/tokens/connection strings, and a **semantic
conflict** (two decisions that contradict each other) stops and asks you. The non-semantic merge
resolves itself (union, keeping both sides).

### From scratch

There's no "create project" command: you bootstrap it with `/mnemo:save-context <slug>` the first
time (Claude asks you for the name and builds the `INDEX.md` + `pending.md`; and if the store
didn't exist, it creates it). Later, on any laptop, `/mnemo:load-context <slug>` resumes where you
left off.

### Machine-tied work

The memory is the same on all your machines, but **some pending items belong to just one** — local
uncommitted code, "push repo X", a service running on *this* laptop. Those items get stamped
`[@<machine>]` in `pending.md`. When you see them **from another machine**, `load-context` marks
them with **⚠** and Claude **won't try to act on them there** (it won't look for a code repo that
isn't on that machine): it tells you *"this belongs to `<machine>`, not here"*.

The machine label is `MNEMO_MACHINE` (if you export it) or the hostname. **Portable** tasks
(implement X, design decisions) aren't stamped — they hold anywhere. This way the same memory works
on all machines without Claude getting confused about *where* each piece of work lives.

### Save reminder (hook included)

It's easy to work a long session and forget `/mnemo:save-context`. The plugin ships a hook that,
after accumulating unsaved edits, suggests running `/mnemo:save-context` (it blocks nothing, just
warns). It counts **unpersisted work**: as soon as you save, the counter resets itself and goes
quiet.

It comes active with the plugin. Tune or turn it off via env:

| Variable | Default | What it does |
|---|---|---|
| `MNEMO_SAVE_EDITS` | `40` | unsaved edits before warning (and how often to re-warn). `0` turns it off |
| `MNEMO_SAVE_TOKENS` | `0` (off) | also warns if the context exceeds N tokens (optional signal; the transcript format is internal to Claude Code) |
| `MNEMO_SAVE_TOKENS_STEP` | `60000` | re-warn interval by tokens |

## Store structure

```
<store>/
  projects/<slug>/INDEX.md     # what it is, scope, status
  projects/<slug>/pending.md   # pending tasks -> "pick up where I left off"
  memories/<id>.md             # atomic notes, multi-tag (the overlap lives here)
  shared/SCHEMA.md             # frontmatter contract
```

Full frontmatter contract: `templates/SCHEMA.md` (copied into the store on the first save).

## Plugin structure

```
mnemo/
  .claude-plugin/plugin.json        # manifest (name, version, hook reference)
  .claude-plugin/marketplace.json   # catalog to install from GitHub
  skills/<command>/SKILL.md         # the /mnemo:* slash-commands
  skills/load-context/card.py       # strict render of the resume card (python3)
  hooks/hooks.json                  # registers the reminder hook
  scripts/suggest-save.js           # the hook (node, no dependencies)
  templates/SCHEMA.md               # frontmatter contract (copied into the store)
```

## Future

The store is files. If someday you have thousands of notes and `grep` falls short, a search index
gets mounted on top (SQLite/MCP) **without changing your files**. Starting with text+git is on
purpose: zero infra, zero lock-in, your data always readable.
