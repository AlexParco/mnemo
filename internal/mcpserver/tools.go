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
					"memories and pending items it has, and what comes next. Use the slug form to re-check a project " +
					"mid-session — it is a fraction of the size of loading it again.",
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
					"frontmatter tags.",
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
	}
}

// memoryTypes are the kinds of fact a memory can hold. The store validates them
// too; here they are an enum so a client can offer them and a wrong one is
// refused before any handler runs.
var memoryTypes = []string{"decision", "constraint", "gotcha", "bug", "reference", "todo"}
