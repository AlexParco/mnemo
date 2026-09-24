# Ideas from other projects

mnemo is not the first attempt at memory for coding agents. This file collects what other people
built, what is worth taking from it, and what was considered and turned down. A source does not
have to be a memory system: anything that changes how mnemo should behave belongs here.

It is a staging area, not a plan. An idea here is not a commitment, and nothing here changes how
mnemo behaves until it moves into the spec that owns it. When that happens, the entry stays and says
where it went, so the same idea does not get re-litigated in three weeks.

## How to add a source

1. Read it, then write an entry under [Sources](#sources): the link, what it actually does, and what
   it is for. Describe the thing, not its README's opinion of itself.
2. Put what is worth taking under [Worth taking](#worth-taking), with what it would cost and which
   document it lands in.
3. If it was considered and turned down, say so under [Turned down](#turned-down) with the reason.
   A rejected idea that nobody wrote down comes back every few weeks.
4. If it raises a decision mnemo has not made, add it to [Decisions it raises](#decisions-it-raises)
   rather than deciding it in passing.

## Worth taking

Ordered by what they return against what they cost.

| # | Idea | Where it lands | Cost | Status |
|---|---|---|---|---|
| 1 | A file about the user, loaded with every project | `shared/ABOUT-ME.md`, plus a line in the save criterion | none beyond the text | not started |
| 2 | Refuse to write a secret, at write time, with the scanner the push already uses | [specs/store.md](specs/store.md), plus a line in the criterion | small: the scanner exists | written into the specs |
| 3 | Search first and fetch detail second, instead of handing over a whole project | [specs/mcp.md](specs/mcp.md) | a decision, then small | decide before the MCP layer |
| 4 | A readable view of a project's memories, grouped by type | [specs/mcp.md](specs/mcp.md) or a command | small | not started |
| 5 | Knowledge graduates: a finished todo that produced a rule becomes a constraint | the save criterion | text | not started |
| 6 | References carry a link to the ticket or runbook they point at | the save criterion | text | not started |
| 7 | Load the card automatically when a session starts | [specs/integrations.md](specs/integrations.md) | blocked on a decision | not started |
| 8 | A marker that keeps something out of the memory for good | the save criterion | text | not started |
| 9 | When nothing is found, the tool says that nothing is saved, and that this is the answer | [specs/mcp.md](specs/mcp.md) | text | written for search; the other empty answers already said it |
| 10 | An agent that asserts something from memory names the memory it comes from | the server instructions and the criterion | text | not started |
| 11 | A memory says what was true when it was written: show the date, and check the code when they disagree | the server instructions, and what search and load return | small | what the date means is written into [specs/store.md](specs/store.md) and the data contract; showing it and the rule about the code are not |
| 12 | Save only what happened: no inferred decisions, and exact values copied, never paraphrased | the save criterion | text | not started |
| 13 | Capture commitments, not musings: a written list of what is never a reason to save | the save criterion | text | not started |
| 14 | Every answer has a cap, and says when it was cut | [specs/mcp.md](specs/mcp.md) | small | written for loading a project and for search |
| 15 | How old a memory is, from git, shown where it is read | [specs/mcp.md](specs/mcp.md) and [specs/store.md](specs/store.md) | small to moderate | not started |

**1. A file about the user.** Other systems keep a profile of the person next to the project notes,
so the agent knows how they work before it is told. mnemo needs no machinery for this: `shared/`
already loads whole with every project, so it is one more file there. It travels between machines
through the hub, which the systems it comes from do not do.

**2. A secret is refused when it is written.** mnemo already enforces this at the only exit, the
push, and there is no way around that scan. But by then the secret is on disk and in a commit. lore
runs the same kind of check before anything is stored, with an override that has to be asked for.
mnemo can do that with the scanner it already has: run the rules over a memory or a pending list
before it is written, refuse with the same redacted finding the push would give, and keep the push
scan as the second line. The criterion says it in words; the write enforces it.

**3. Search first, fetch second.** The card was cut down to open tasks for exactly this reason, and
the same question now applies to `mnemo_load_project` with full detail: it returns the body of every
memory of the project, which grows without limit.

claude-mem gives the shape of the answer. It answers in three steps: a search that returns little
more than identifiers, a view of what surrounds them, and the full text of only the ones that were
chosen. mnemo already has two of those three: search returns summaries, and reading one memory
returns its body. What is missing is making that the path a tool is steered onto, and stopping the
default from being "here is everything".

**4. A readable view.** mnemo stores one fact per file, which is right for tagging, diffing and
overlap, but leaves nothing a person can open and read straight through. The answer is to generate
the view, grouped by type, not to keep a second copy of the same facts as other systems do.

**5. Graduation.** A todo that was done and left a rule behind should not be deleted; it becomes a
constraint. mnemo has the types but says nothing about the path between them.

**6. References with a link.** The type exists and the date exists. What is missing is the habit of
putting the ticket or runbook URL in the body, which is what makes a reference worth having.

**7. Automatic load at the start.** The plugin already ships a hook, so the mechanism is there, and
what it would inject is the generated card rather than a summary someone maintains by hand. The cost
is not the hook: it is knowing which project the current directory belongs to. mnemo decided that
slugs are exact and are never guessed, so this needs the association to be stored somewhere, or the
injection stays quiet when it cannot tell. See the decision below.

**8. A marker that keeps something out.** claude-mem lets the user wrap text in a tag so it never
enters the memory at all. mnemo's exposure is smaller, because nothing is captured automatically and
an agent writes only what it decides is worth keeping, but the need is the same: a way to say "not
this" that does not depend on the agent's judgement. The cheapest version is a line in the criterion
saying that anything the user marks that way is never written, and that the marker itself is not
worth saving either.

### Memory is a place to hallucinate

Ideas 9 to 12 come from guidance on hallucination rather than on memory, and they matter more here
than anywhere, for one reason: **a hallucination that gets saved stops being one.** An agent that
distils a session and writes down a decision nobody made has produced a memory. It syncs to the
user's other machines, other agents load it, and from then on it is cited as a fact with a file to
point at. Every technique below is aimed at one of the two ends of that pipe.

**9. Nothing found means nothing saved.** The standard advice is to give the model permission to
say it does not know. In mnemo that permission belongs in the tool's answer, which is where the
temptation is: an empty search should say that nothing is saved about this, and that saying so is
the correct reply, rather than leaving a silence to be filled from what the model half remembers.
The same goes for a memory id that does not exist and a slug that matches no project.

**10. Name the memory.** The general form is to make claims auditable by citing what supports them,
and to retract what has no support. mnemo makes that unusually cheap: every memory has a stable id
that reads like a sentence, and the user can open the file. An agent that says "we decided to rotate
tokens every fifteen minutes" should say which memory says so. A claim with no id behind it is, by
the same rule, not something mnemo told it.

**11. True when written.** A memory is a statement with a date, not a standing fact. The most
plausible wrong answer a memory system produces is a correct note about code that has since changed.
The date is already in every file; what is missing is showing it where the agent reads the note,
and saying in the instructions that when memory and the code disagree, the code is what is true now
and the memory is what needs updating. One more thing to say plainly, which mex's own files do:
`updated` is the day the note was recorded, not the day the thing it describes happened. An agent
that reads a date as a historical event is inventing history.

**12. Save only what happened.** This is the write side, and the one no other source mentions. The
criterion already says what is worth keeping. It should also say what may be written at all: what
was actually decided or observed in this session, not what the agent concluded would have been
sensible. Values that are wrong when approximated — ports, hostnames, versions, limits — are copied
exactly. And when it is unclear whether something was decided or only discussed, the agent asks
rather than saves.

**13. Commitments, not musings.** lore's skill is the best articulation seen so far of the line
between the two, and it draws it with a list of things that are never a reason to save: the prompt
ends in a question mark; it carries a hedge such as "maybe" or "what if"; it is a counterfactual; the
user said not to save it; it is sarcasm. Its one-line test is whether the user is committing to a
stance or floating an idea, and it names the failure directly: paraphrasing the user's question into
a statement and saving that is a hallucinated capture. This is the concrete form of idea 12, and it
is text.

**14. Every answer has a cap, and says so.** mex bounds every read: the note log returns at most so
many entries and so many bytes, and its JSON says when the result was cut. The rule that comes with
it is the one that matters: a filtered result that came back empty does not prove that nothing
matches in the full history. mnemo's search has a limit already, and load has none. Both should say
what they left out, because an agent that gets a silently truncated answer will assert absence, and
that is idea 9 failing from the other side.

**15. Age, from git.** mex flags a file as drifting after so many days or so many commits without a
change, with a warning and an error threshold, and shows it as a review signal rather than a verdict.
mnemo has git under every memory, so the age of a note is one command away and costs nothing to
store. Shown next to the note when it is loaded or found, it is the cheapest possible form of idea
11, and it needs no code analysis to work.

**Where these have to live.** The anti-hallucination skill's own README makes the point against
itself: a skill is loaded on demand, so there is a chance it is not loaded at the moment it is
needed, which is why it recommends the always-loaded rules file instead. mnemo already sends its core
rules with every connection, in the server's instructions, and that is where these lines go. They
have to be short, because that text is paid for on every session.

## Decisions it raises

- **How big may a tool's answer be?** Related to idea 3. It has to be settled before the MCP layer
  is written, because it changes what `mnemo_load_project` returns by default.
- **Does a directory know its project?** Related to idea 7. Storing that association is a new thing
  to keep in sync; not storing it means the automatic load often has nothing to say.
- **Who owns the memory, the person or the team? Decided: the person.** Written down in
  [cli.md](cli.md#whose-memory-this-is), with what it buys and what it costs, and repeated where
  bootstrap gives a nested store its own repository. Others answer "the team": notes committed
  inside the project's repository, reviewed in pull requests. mex is the strongest case for that
  answer reviewed so far:
  the same plain files and the same git-only transport as mnemo, but inside the project, with people
  approving what becomes canonical, and with real adoption behind it. It also shows the price: every
  project carries the scaffold, and memory that spans two projects has nowhere to live.
- **Is what an agent did worth remembering by itself?** mnemo says no: a memory is written when
  someone decides the fact is worth keeping. claude-mem says yes, and records tool usage as it
  happens, compressing it afterwards. The second answer buys recall of things nobody thought to
  write down, and pays for it with a database of everything that was ever done. Worth stating
  deliberately, because it is the line between a memory and a log.
- **Should a memory say where it came from?** Whether the user said it, the agent read it in the
  code, or the agent inferred it are three different strengths of evidence, and today they look the
  same once saved. An optional frontmatter field would record it without changing the format, since
  unknown keys already survive every rewrite. The cost is one more thing for an agent to get right,
  and a field that is filled in carelessly is worse than none. lore shows one full shape of this:
  who wrote it, where it came from, when it became true, when it stopped being true, and which
  record replaced it. Two of those already exist in mnemo as `author` and `updated`; the mailbox
  identity could fill `author` with the agent's own name. The one worth the most is "when it stopped
  being true": marking a memory as no longer valid, rather than rewriting or deleting it, keeps the
  fact that it was once believed, which git history has but no agent reads. mex adds a distinction
  worth keeping if this is ever done: who or what produced the note is one thing, and what evidence
  it rests on is another. Its source code says the two "get collapsed constantly and should not be".
  mex agrees with lore on the other point too: a decision is superseded, never deleted.
- **How eagerly should an agent save on its own?** mex gives the user one knob with three settings:
  only what is significant, in batches at task boundaries, or only when asked. mnemo's agents save
  when the user runs a save or jots a note, and never on their own initiative. That is the quietest
  setting, chosen by default. Whether the other two are wanted is a question for after the first
  weeks of use, not before.
- **Are constraints always loaded?** lore compiles its must-rules into the file the agent reads at
  every start, and fetches everything else on demand. mnemo's card was just cut down to open tasks,
  and its constraints are reached by loading or searching. A constraint is exactly the kind of note
  an agent must not have to think to look for. Either the card stays as decided and constraints stay
  a load away, or a small always-loaded set exists. This is a decision, and it was made one way a
  few days ago; lore is a reason to check it, not to reverse it.

## Turned down

- **A summary file maintained by hand**, such as a `core.md` of pointers that the agent rewrites.
  It drifts away from the data it summarises, and nothing catches that. mnemo's card is generated,
  so it cannot lie.
- **Organising by topic instead of by project.** Projects with overlap already express a topic that
  spans two projects, and they also filter, which a topic tree does not.
- **Writing memory in the background** so it does not block the chat. It buys nothing here — the
  writes are local and fast — and it adds failure modes where a save silently did not happen.
- **Keeping memory inside the project's repository**, committed with the code. Not worse, but a
  different product: it changes who owns the memory. Note for whoever revisits this: mnemo detects a
  store created inside another repository and gives it a repository of its own, which is the
  opposite of what a team sharing one repo would want.
- **Capturing tool usage automatically** and summarising it later. It is a different product: a log
  of what happened, not a record of what was decided. It also means everything typed near an agent
  ends up in a database, which is a promise mnemo would then have to keep.
- **Embeddings and a vector database.** The first three sources all went the other way on purpose,
  and this one is the exception. What it costs is visible in its own install: a second runtime, a
  Python package manager, a vector store and a background service. mnemo's answer to "find the note
  about X" is search over plain files, and the day that stops being enough, an index can be built on
  top without changing a single file.
- **A background service with an HTTP API and a viewer.** mnemo runs a server only to let other
  machines reach one, and has no interface on purpose.
- **Syncing through someone else's service.** mnemo syncs through a git remote the user already
  owns. The need is real; the third party is not.
- **Running a prompt several times and comparing, or asking for step-by-step reasoning,** as ways to
  catch a wrong answer. They work, but they are things whoever calls the model does. A memory store
  does not call the model, so there is nowhere in mnemo for them to go.
- **Shipping general rules about honesty and sources.** That is a job for the user's own rules file,
  and there are projects that do only that. mnemo's rules cover how memory is used, and stop there.
- **A typed database of memories, rules, decisions, tasks and agent runs.** lore has sixty-odd
  entity types behind an ORM, a terminal interface, a benchmark engine and mandatory task and run
  bookkeeping on every non-trivial turn. Much of it has no callers. mnemo's six memory types and one
  pending list are the whole model, on purpose.
- **Compiling memory into the agent's rules file.** lore writes an import into `CLAUDE.md` and
  renders its pinned rules there. It works for one tool, it puts generated content in a file the
  user also edits, and it makes the rules file the memory. mnemo keeps memory in its own store and
  loads it through tools, which is the same for every agent.
- **An instruction block that shouts.** lore's directive opens with a stop sign and an order to do
  nothing before invoking it, and its own source says this is because softer wording was ignored.
  That is a cost paid on every turn to work around a delivery problem. mnemo's rules travel in the
  server instructions and tool descriptions, where they do not need volume.
- **Anything verbatim from lore.** Its licence is PolyForm Perimeter, which is not open source and
  forbids competing products. Ideas only; not a line of code.
- **A code graph that pins notes to symbols.** mex parses the code with tree-sitter into a local
  SQLite graph so a claim can point at a function and be flagged when the function moves. It is the
  bulk of its complexity, it is per-language, it fixes its runtime to a Node build with a specific
  SQLite option, and it pays off only for prose that names symbols. mnemo's answer to drift is the
  date and the age (ideas 11 and 15), which cost nothing.
- **A review workflow with members, proposals and handoffs.** mex's inbox and relay are a governance
  process for a team: drafts, approvals, claims, signed previews. mnemo's user is one person with
  several agents and several machines, and the mailbox already carries a handoff without a state
  machine around it.
- **Telemetry on by default.** mex reports usage to a third party unless told not to. Nothing in
  mnemo phones anywhere, and that stays.

## Sources

### claude-memory-skill

<https://github.com/hanfang/claude-memory-skill>

Markdown under `~/.claude/memory/`: a `core.md` of summaries and pointers that is always loaded, a
`me.md` with the user's profile, detail in `topics/`, and per-project files. Driven by a skill and a
hook, with `/mem show` and `/mem forget`. Saving is triggered by the user saying "remember that…",
and a background agent decides where it goes. Deliberately no embeddings and no semantic search:
retrieval is grep. Nothing about other machines.

**Taken:** the profile file (1). **Turned down:** the hand-maintained summary, topics instead of
projects, background writes.

### project-memory

<https://github.com/SpillwaveSolutions/project-memory>

Four markdown files inside the project's own repository, under `docs/project_notes/`: bugs,
decisions, key facts and a work log. Dated bullets, no database. Invoked as a skill, and written to
look like ordinary engineering documentation so it is reviewed like any other file in a pull
request. Says plainly that credentials must never be stored.

**Taken:** the readable view (4), credentials in the criterion (2), references with links (6).
**Raised:** who owns the memory.

### How I finally sorted my Claude Code memory

<https://www.youngleaders.tech/p/how-i-finally-sorted-my-claude-code-memory>

An account of replacing one flat file with a small tree: an index read at the start, general
conventions, and directories for tools and domains, plus per-project memory. A hook injects memory
before tool calls. Its argument is that structure beats volume, that routing rules do not belong in
memory because they spend the context budget, and that the author will not depend on something whose
workings are hidden.

**Taken:** the size budget (3), graduation (5), automatic load at the start (7).

### claude-mem

<https://github.com/thedotmack/claude-mem>

The largest of the four, and the only one that is a system rather than a convention. Five lifecycle
hooks capture what an agent does as it does it; a local service written for the Bun runtime keeps
the results in SQLite with full-text search, alongside a Chroma vector store for semantic search; an
AI pass compresses raw observations into summaries. Retrieval goes through four MCP tools and a
skill, in three steps: search for identifiers, look at what surrounds them, then fetch the full text
of only what was chosen. It installs its own runtime and Python package manager, offers optional
sync to a hosted service, and ships a web viewer. Users can wrap text in a tag to keep it out.

**Taken:** the three-step retrieval, which sharpens idea 3; the marker that keeps something out (8).
**Turned down:** automatic capture of tool usage, embeddings and the stack they need, the background
service with a viewer, syncing through a third party. **Raised:** whether what an agent did is worth
remembering by itself.

### anthropic-anti-hallucinate-skills

<https://github.com/instantX-research/anthropic-anti-hallucinate-skills>

Not a memory system: a set of behavioural guidelines for Claude Code, drawn from Anthropic's own
explanation of why models hallucinate. Five principles for the model — honesty over helpfulness,
never fabricate a source, calibrate confidence, take extra care with facts, dates, names and niche
topics, and stop to ask whether it actually knows — and six tactics for the person: ask for sources
and check they support the claim, give permission not to know, probe confidence, verify in a fresh
chat, cross-reference anything critical, and follow up on what sounds too convenient. It ships as a
rules file and as a plugin, and recommends the rules file, because a skill loads on demand and may
not load when it matters.

**Taken:** permission not to know, placed in the tool's answer (9); the care over dates, names and
numbers, turned into copying exact values (12); and the point about where rules must live.
**Turned down:** shipping general honesty rules.

### Reduce hallucinations

<https://platform.claude.com/docs/en/test-and-evaluate/strengthen-guardrails/reduce-hallucinations>

Anthropic's guide. Three basic strategies: let the model say it does not know; for long documents,
have it extract word-for-word quotes before doing anything with them; and make a response auditable
by citing a quote for each claim, retracting any claim that has none. Four advanced ones: reasoning
step by step, running the same prompt several times and comparing, feeding an answer back for
verification, and restricting the model to the documents it was given. It closes by saying these
reduce hallucinations and do not eliminate them.

**Taken:** citing what supports a claim, as naming the memory (10); restricting to what was
provided, which is what "search the memory rather than guessing" already is, extended to say what
to do when the search comes back empty (9). **Turned down:** the techniques that belong to whoever
calls the model.

### lore

<https://github.com/thesatellite-ai/lore>

Reviewed from its source, docs, skill bundle and release history rather than its README alone. A
single static Go binary and a Claude Code skill. Memory is typed records in one SQLite file per
repository, gitignored: rules with a severity, decisions with a rationale, recurring traps,
free-form memories, plus tasks and agent runs. Its bet, in its words, is that the agent already reads
`CLAUDE.md` at every start, "so lore makes that file the memory": a render step writes the must-rules
and critical traps into a file that `CLAUDE.md` imports, deterministically, and everything else is
reached with a full-text search command. Capture is explicit, driven by the skill's rules about what
counts as a commitment. No MCP server, no hooks, no embeddings in use, no hosted service, no sync:
each developer keeps their own database, and only the rendered file travels with the repo. It scrubs
secrets before writing, records who wrote each record and where it came from, and can mark a record
as no longer valid or as superseded by another. Licence is PolyForm Perimeter, which is not open
source. One author, ten releases in two weeks in May, no code change since, one star.

**Taken:** secret scanning at write time (2), the commitment-versus-musing list (13). **Raised:**
the full shape of provenance and "no longer valid", and whether constraints are always loaded.
**Turned down:** the typed database and everything around it, compiling memory into the rules file,
the shouting directive, and any code at all.

**Not verified:** the binary was not run; whether the skill triggers reliably; whether the audit
verification and hash chain the glossary mentions exist in the CLI. The reviewer could not find them.

### mex

<https://github.com/mex-memory/mex>

Reviewed from its README, source, templates, skills, compatibility and telemetry notes, release
history and package registry. "Shared project memory for engineers and their coding agents", kept
in `.mex/` inside the project's repository as markdown with frontmatter and JSONL logs, committed and
shared through the project's own git remote: "MEX never pushes or pulls." Two local SQLite indexes,
a code graph built with tree-sitter and a full-text index, are rebuilt on each machine and never
shared; the README calls them "rebuildable local SQLite views, not shared sources of truth". Agents
are steered by instruction files and two skills, for Claude Code and Codex, with no hooks; opencode
and others get an instruction file. The AI passes that populate and resync the scaffold shell out to
the user's own `claude`, `codex` or `opencode`. Team features add members, proposals a person
approves, and handoffs that are files, not messages: "Publishing writes files to Alex's checkout; it
does not notify Sam." Fifteen drift checkers, staleness thresholds from git, provenance kept apart
from sources, supersession instead of deletion, and bounded reads that report truncation. Telemetry
to a third party is on by default. Node 22.5 or newer with a SQLite build that has full-text search.
MIT, 1,645 stars, releases every few weeks, several contributors. A hosted, admin-governed version
is advertised on its website and is not in the repository.

**Taken:** answers with a cap that say when they were cut (14), age from git (15), and the point
that a date records when a note was written (11). **Raised:** the who-versus-evidence split in
provenance, and how eagerly an agent should save on its own. **Turned down:** the code graph, the
review workflow, the interfaces, telemetry on by default, and memory inside the project's repo.

**Not verified:** the reviewer did not run it, and did not trace the wiki synthesis pipeline end to
end. Whether the hosted version exists or is planned could not be told from the repository.

## Where mnemo stands

Four of the six memory systems keep plain text as the source of truth, edited by hand, shared as
files. That is the same bet mnemo makes. One went to embeddings and a service stack, and what that
costs is legible in its dependencies. One, lore, keeps a database but refuses embeddings and
hosting. mex keeps plain files canonical and adds disposable local indexes on top, which is a third
position: the files stay the truth, and the database is allowed because it can be thrown away.

**Crossing machines is not unique to mnemo,** and neither is doing it over the user's own git
remote: claude-mem does it through a hosted service, and mex does it exactly the way mnemo does,
with plain files and ordinary push and pull, no third party. What is still unmatched is the
combination: one store of the person's own that spans projects, shared across machines the person
owns, reached by agents in different tools, with a mailbox so one agent can leave another a message
and get an answer back. mex's handoffs are files a colleague finds later, and its README says so.
Nothing reviewed so far has a message between agents.

**On distribution,** mnemo and lore are the two that ship as one static binary. The one with a
service stack installs a second JavaScript runtime, a Python package manager, a vector store and a
background worker to do its job.

**On wrong and stale memories,** the READMEs of the first three repositories were searched directly
and say nothing about what happens when the thing being saved is wrong, or when a note outlives the
code it describes. lore names the hallucinated capture in its skill, scrubs secrets before writing,
and can mark a record as no longer valid. mex goes furthest: staleness from git, drift checks
against the code, provenance apart from evidence, supersession instead of deletion, and instructions
that forbid inventing an author, a date or an event. Shared memory makes this problem worse, not
better: a wrong note no longer misleads one chat, it misleads every agent on every machine. Ideas 9
to 15 are mnemo's answer; the two that take it seriously reached much the same conclusions.
