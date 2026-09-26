package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Handler answers one tool call. It is given the arguments already validated
// against the tool's schema, so it can read a field without checking its type.
type Handler func(ctx context.Context, call *Call, args Args) *answer

// Definition is one tool as an agent sees it, with what answers it.
//
// The definition is data on purpose. The same list is served by this process
// over stdio and, for a connected machine, by a server that answers on its
// behalf. Two hand-maintained lists would drift, and an agent would be offered a
// tool that is not there or miss one that is.
type Definition struct {
	Tool   *mcp.Tool
	Handle Handler
}

// Input schemas are written out rather than inferred from a Go struct. The
// descriptions are part of the contract — they are what an agent reads to decide
// whether to call a tool at all — and enums and bounds cannot be expressed in a
// Go struct tag. Writing the schema is therefore writing the contract once,
// instead of writing it in a tag and correcting it afterwards.
type schema = map[string]any

// object builds an input schema. Properties not named in required are optional.
func object(required []string, properties schema) schema {
	if required == nil {
		required = []string{}
	}
	return schema{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func text(description string) schema {
	return schema{"type": "string", "description": description}
}

func choice(description string, options ...string) schema {
	values := make([]any, len(options))
	for i, option := range options {
		values[i] = option
	}
	return schema{"type": "string", "description": description, "enum": values}
}

func number(description string, min, max int) schema {
	return schema{"type": "integer", "description": description, "minimum": min, "maximum": max}
}

func boolean(description string) schema {
	return schema{"type": "boolean", "description": description}
}

// atLeastOne is a list that may not be empty, for a field where an empty array
// would otherwise reach the store and be refused there.
func atLeastOne(description, itemDescription string) schema {
	out := list(description, itemDescription)
	out["minItems"] = 1
	return out
}

func list(description, itemDescription string) schema {
	return schema{
		"type":        "array",
		"description": description,
		"items":       schema{"type": "string", "description": itemDescription},
	}
}

// truth is a pointer to a bool, which is how the protocol tells a hint that was
// set to false from one that was not given at all.
func truth(value bool) *bool { return &value }

// idempotent marks a tool that can be called again with the same arguments and
// leaves the store in the same state. It says nothing about destruction: an
// upsert replaces a name that was there, and claiming otherwise would be a
// promise a client shows the user.
func idempotent() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{IdempotentHint: true}
}

// adds marks a tool that only ever adds. A client uses it to decide what to
// ask the user about before calling, so saying "not destructive" of something
// that overwrites would be a lie a person acts on.
func adds() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{DestructiveHint: truth(false)}
}

// destructive marks a tool that can replace or remove what is there.
func destructive() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{DestructiveHint: truth(true)}
}

// readOnly marks a tool that never changes the store. A client uses it to decide
// what to ask the user about, so it is a promise, not a hint we can be loose with.
func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true}
}

// memoryTools are the tools that read and write the store. The mailbox adds its
// own, and both lists are served together.
func memoryTools() []Definition {
	return []Definition{
		{
			Tool: &mcp.Tool{
				Name: "mnemo_status",
				Description: "Where things stand. With no arguments: the memory store itself — its path, whether it " +
					"is wired to a hub (git remote), how many commits are waiting to be pushed, and this machine's " +
					"label. With a `slug`, it also reports where that project stands: status, services, how many " +
					"memories and pending items it has, and what comes next.",
				Annotations: readOnly(),
				InputSchema: object(nil, schema{
					"slug": text("Project slug. Adds a compact summary of that project; omit it for the store alone."),
				}),
			},
			Handle: handleStatus,
		},
		{
			Tool: &mcp.Tool{
				Name: "mnemo_list_projects",
				Description: "Overview of every project in the mnemo memory store: slug, status, how many memories " +
					"each has, and which services it touches. Read-only. Use it when the user asks what projects " +
					"they have, or when they name a project loosely and you need the exact slug before loading it.",
				Annotations: readOnly(),
				InputSchema: object(nil, schema{}),
			},
			Handle: handleListProjects,
		},
		{
			Tool: &mcp.Tool{
				Name: "mnemo_load_project",
				Description: "Load a project's persistent memory so work can resume where it was left off: " +
					"decisions, constraints, gotchas, bugs and pending tasks saved in earlier sessions, possibly on " +
					"another machine. Requires an exact slug — call mnemo_list_projects first if you do not have " +
					"one, and never guess. The FIRST content block is a rendered resume card: show it to the user " +
					"verbatim, and nothing else, unless they ask for the detail. The remaining blocks are for you, " +
					"not for printing.",
				Annotations: readOnly(),
				InputSchema: object([]string{"slug"}, schema{
					"slug": text("Exact project slug, as listed by mnemo_list_projects."),
					"lang": choice("Language for the card. Defaults to the configured language, else English.",
						"en", "es"),
					"detail": choice("\"full\" (the default) returns the card, the project's memories and the "+
						"shared conventions — what you want the first time. Long projects come back as a complete "+
						"index with as many bodies as fit; read the rest with mnemo_read_memory. \"card\" returns "+
						"only the card, for re-showing where a project stands without re-sending detail that is "+
						"already in your context.", "full", "card"),
				}),
			},
			Handle: handleLoadProject,
		},
		{
			Tool: &mcp.Tool{
				Name: "mnemo_search_memories",
				Description: "Search saved facts across the mnemo memory store — decisions, constraints, gotchas, " +
					"bugs, references, todos. Use it when the user asks what was decided about something, and " +
					"always before writing a new memory, so an existing note gets updated instead of duplicated.",
				Annotations: readOnly(),
				InputSchema: object(nil, schema{
					"query":   text("Case-insensitive text match over id, body, tags, services and projects."),
					"project": text("Restrict to memories tagged with this project slug."),
					"type":    choice("Restrict to memories of this type.", memoryTypes...),
					"limit":   number("Maximum results (default 25).", 1, 200),
				}),
			},
			Handle: handleSearch,
		},
		{
			Tool: &mcp.Tool{
				Name: "mnemo_read_memory",
				Description: "Read a single memory in full, by id (the file name without .md), including its " +
					"frontmatter tags. Call it before overwriting or superseding one, so you merge instead of " +
					"dropping what it holds.",
				Annotations: readOnly(),
				InputSchema: object([]string{"id"}, schema{
					"id": text("Memory id, e.g. 'checkout-customer-immutable'."),
				}),
			},
			Handle: handleReadMemory,
		},
		{
			Tool: &mcp.Tool{
				Name: "mnemo_guide",
				Description: "Return mnemo's usage criterion as markdown, for the user to paste into their agent's " +
					"rules file (AGENTS.md, CLAUDE.md). Use it when the user is setting mnemo up, or when they ask " +
					"how their agent should use it. Tools that surface MCP prompts already get these rules that " +
					"way; this is for the ones that do not.",
				Annotations: readOnly(),
				InputSchema: object(nil, schema{
					"target": choice("Which rules file the snippet is destined for. Only changes the wording of "+
						"the instruction, not the rules.", "agents", "claude", "generic"),
				}),
			},
			Handle: handleGuide,
		},
		{
			Tool: &mcp.Tool{
				Name: "mnemo_bootstrap",
				Description: "Create the mnemo memory store, or adopt one that already exists on the hub. Safe to " +
					"call repeatedly: it only adds what is missing. The write tools call it themselves, so use " +
					"this only when the user explicitly wants to set up or inspect the provisioning — for " +
					"instance on a new machine, after setting the hub remote.",
				Annotations: idempotent(),
				InputSchema: object(nil, schema{}),
			},
			Handle: handleBootstrap,
		},
		{
			Tool: &mcp.Tool{
				Name: "mnemo_upsert_project",
				Description: "Create a project in the memory store, or update its name, status, services or " +
					"description. Creating one is deliberate: never invent a project to make a write succeed — if " +
					"a slug does not exist, ask the user whether to create it or fix the slug. Use `services` for " +
					"the parts of ONE project (repos, modules, areas); two repos of the same product are one " +
					"project with two services, not two projects.",
				Annotations: idempotent(),
				InputSchema: object([]string{"slug"}, schema{
					"slug": text("Kebab-case identity of the project. Stable: renaming it later is a separate " +
						"operation."),
					"name":     text("Readable name. Required when creating."),
					"status":   choice("Where the project stands.", projectStatuses...),
					"services": list("Areas, repos or modules this project touches.", "One area, repo or module."),
					"description": text("What the project is, its goal and scope — context not derivable from " +
						"the code."),
				}),
			},
			Handle: handleUpsertProject,
		},
		{
			Tool: &mcp.Tool{
				Name: "mnemo_write_memory",
				Description: "Persist ONE fact to the memory store: a decision made, a constraint that must not " +
					"be broken, a gotcha, a known bug, a useful reference. One fact per file — if a note mixes " +
					"two topics, write two memories. Do NOT save what is already in the code, in git history, or " +
					"is ephemeral to this conversation; when in doubt, save less. Search with " +
					"mnemo_search_memories first and update the existing note instead of adding a near-duplicate. " +
					"`projects` is for genuinely different projects that share the fact (overlap); parts of a " +
					"single project go in `services`. Writes are not committed — call mnemo_commit afterwards.",
				Annotations: destructive(),
				InputSchema: object([]string{"id", "projects", "type", "body"}, schema{
					"id": text("Kebab-case id derived from the content, and the file name. Choose it yourself; " +
						"it must be unique and self-explanatory."),
					"projects": atLeastOne("Project slugs this fact belongs to. Usually one. Every slug must "+
						"already exist.", "One project slug, kebab-case."),
					"type": choice("What kind of fact this is.", memoryTypes...),
					"body": text("The fact, in markdown. Self-explanatory and concise. Link related memories " +
						"with [[other-id]]."),
					"services": list("Which repo/module/area within the project this touches.",
						"One repo, module or area."),
					"tags":   list("Words to find it by later.", "One tag."),
					"author": text("Defaults to the store's git user.name."),
					"overwrite": boolean("Replace an existing memory in place. Only for correcting a note that " +
						"is still true: a typo, a missing detail. If the fact itself CHANGED, leave this off and " +
						"use supersedes instead. Read the old one first and merge; a fresh body replaces " +
						"everything."),
					"supersedes": text("The id of a DIFFERENT memory this one replaces because the fact changed. " +
						"This is the normal way a fact changes: that memory is kept and marked, not deleted, " +
						"because someone asking why the answer changed needs both. Overwriting is only for a note " +
						"that is still true."),
				}),
			},
			Handle: handleWriteMemory,
		},
		{
			Tool: &mcp.Tool{
				Name: "mnemo_write_pending",
				Description: "Replace `pending.md` for a project — the living state a later session resumes " +
					"from. This REPLACES the whole file. Start from `pending_file` in what mnemo_load_project " +
					"returned, which is the file itself; do NOT rebuild it from the parsed `pending` sections, " +
					"which drop prose and cut each item at 100 characters. Sections are free-form; the resume " +
					"card lists the unchecked items of all of them. Finish an item by writing `- [x]`, never by " +
					"deleting it — that record is what stops the next session proposing it again. Do not prune " +
					"`## Done`. Stamp `[@<machine>]` only on work physically bound to one machine; when unsure, " +
					"do not stamp.",
				Annotations: destructive(),
				InputSchema: object([]string{"slug", "content"}, schema{
					"slug":    text("The project whose pending list this replaces."),
					"content": text("The complete new pending.md, in markdown."),
				}),
			},
			Handle: handleWritePending,
		},
		{
			Tool: &mcp.Tool{
				Name: "mnemo_commit",
				Description: "Commit everything written to the memory store since the last commit — normally " +
					"once, after a batch of memory and pending writes, so one session becomes one commit. The " +
					"commit stays local: pushing it to the hub is a separate step, and until then the user's " +
					"other machines do not see it.",
				Annotations: adds(),
				InputSchema: object([]string{"message"}, schema{
					"message": text("Commit message, one line, shaped `<verb>(<slug>): <what the session " +
						"produced>` — e.g. \"save(busy-api): token rotation decisions\"."),
				}),
			},
			Handle: handleCommit,
		},
	}
}

// projectStatuses are where a project can stand.
var projectStatuses = []string{"active", "paused", "done"}

// memoryTypes are the kinds of fact a memory can hold. The store validates them
// too; here they are an enum so a client can offer them and a wrong one is
// refused before any handler runs.
var memoryTypes = []string{"decision", "constraint", "gotcha", "bug", "reference", "todo"}
