package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexParco/mnemo/internal/criterion"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect runs a real server and a real client over an in-memory transport.
//
// The handlers are not called directly here on purpose. Everything between a
// handler and an agent — the schema, the validation, the block order, the error
// flag — is the SDK's, and testing underneath it would prove that mnemo's
// functions work while an agent gets something else.
func connect(t *testing.T, call *Call) *mcp.ClientSession {
	t.Helper()
	serverSide, clientSide := mcp.NewInMemoryTransports()

	// Servers connect first; the transport documents it.
	session, err := New("v0.0.0-test", call).Connect(t.Context(), serverSide, nil)
	if err != nil {
		t.Fatalf("starting the server: %v", err)
	}
	t.Cleanup(func() { _ = session.Wait() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	connected, err := client.Connect(t.Context(), clientSide, nil)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() { _ = connected.Close() })
	return connected
}

func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

// annotated is the tool table of docs/specs/mcp.md: `ro` is read-only, `idem`
// is idempotent, `add` is destructiveHint false and `destr` is true. A hint left
// unset is not the same as one set to false, so it is written out here too.
var annotated = map[string]struct {
	readOnly    bool
	idempotent  bool
	destructive *bool
}{
	"mnemo_status":           {readOnly: true},
	"mnemo_list_projects":    {readOnly: true},
	"mnemo_load_project":     {readOnly: true},
	"mnemo_search_memories":  {readOnly: true},
	"mnemo_read_memory":      {readOnly: true},
	"mnemo_guide":            {readOnly: true},
	"mnemo_bootstrap":        {idempotent: true},
	"mnemo_sync":             {idempotent: true},
	"mnemo_resolve_conflict": {destructive: truth(true)},
	"mnemo_rebase":           {destructive: truth(true)},
	"mnemo_push":             {destructive: truth(false)},
	"mnemo_rename":           {destructive: truth(true)},
	"mnemo_forget":           {destructive: truth(true)},
	"mnemo_upsert_project":   {idempotent: true},
	// write_memory takes `overwrite`, which replaces a file. The hint is static
	// per tool, so it has to describe the worst the tool can do: a client that
	// skips confirmation for non-destructive calls would otherwise approve it.
	"mnemo_write_memory":  {destructive: truth(true)},
	"mnemo_write_pending": {destructive: truth(true)},
	"mnemo_commit":        {destructive: truth(false)},
}

func sameHint(got, want *bool) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

func hint(value *bool) string {
	if value == nil {
		return "unset"
	}
	if *value {
		return "true"
	}
	return "false"
}

func TestAClientSeesTheToolsAndTheirShape(t *testing.T) {
	session := connect(t, project(t, ""))

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	offered := map[string]*mcp.Tool{}
	for _, tool := range listed.Tools {
		offered[tool.Name] = tool
	}
	for _, definition := range memoryTools() {
		tool, ok := offered[definition.Tool.Name]
		if !ok {
			t.Errorf("%s was defined but is not offered", definition.Tool.Name)
			continue
		}
		if tool.Description != definition.Tool.Description {
			t.Errorf("%s reaches the client with a different description", tool.Name)
		}
		// The annotations are a promise a client acts on when it decides what
		// to ask the user about before calling, so each one is checked against
		// what the tool table in docs/specs/mcp.md says, not against itself.
		want, known := annotated[tool.Name]
		if !known {
			t.Errorf("%s is not in the expected annotations; say what it promises", tool.Name)
			continue
		}
		got := tool.Annotations
		if got == nil {
			t.Errorf("%s reaches the client with no annotations", tool.Name)
			continue
		}
		if got.ReadOnlyHint != want.readOnly || got.IdempotentHint != want.idempotent {
			t.Errorf("%s: readOnly=%v idempotent=%v, want %v and %v",
				tool.Name, got.ReadOnlyHint, got.IdempotentHint, want.readOnly, want.idempotent)
		}
		if !sameHint(got.DestructiveHint, want.destructive) {
			t.Errorf("%s: destructive hint is %s, want %s", tool.Name, hint(got.DestructiveHint), hint(want.destructive))
		}
	}
	if len(listed.Tools) != len(memoryTools()) {
		t.Errorf("%d tools offered, %d defined", len(listed.Tools), len(memoryTools()))
	}
}

// The schema is not decoration: a client's argument is checked before any
// handler runs, so a handler never has to defend itself against a wrong type.
func TestTheSchemaIsEnforcedBeforeAnyHandlerRuns(t *testing.T) {
	session := connect(t, project(t, "## In progress\n\n- [ ] work\n"))

	// Every case checks that the SCHEMA stopped the call, not merely that
	// something did. A handler that ran and refused for its own reasons also
	// sets isError, and three of these used to pass that way.
	refusedByTheSchema := func(t *testing.T, what string, tool string, args map[string]any) {
		t.Helper()
		result := call(t, session, tool, args)
		if !result.IsError {
			t.Fatalf("%s was accepted", what)
		}
		said := strings.Join(textOf(t, result), "\n")
		if !strings.Contains(said, "validating") {
			t.Fatalf("%s was refused, but not by the schema: %s", what, said)
		}
	}

	t.Run("a missing required argument", func(t *testing.T) {
		refusedByTheSchema(t, "a load with no slug", "mnemo_load_project", map[string]any{})
	})
	t.Run("a value outside the enum", func(t *testing.T) {
		refusedByTheSchema(t, "an unsupported language", "mnemo_load_project",
			map[string]any{"slug": "shop", "lang": "fr"})
	})
	t.Run("a limit outside its bounds", func(t *testing.T) {
		refusedByTheSchema(t, "a limit above the maximum", "mnemo_search_memories", map[string]any{"limit": 500})
	})
	t.Run("an argument that is not in the schema", func(t *testing.T) {
		refusedByTheSchema(t, "a misspelt argument", "mnemo_status", map[string]any{"slugg": "shop"})
	})

	// The write tools were not covered at all, and they are the ones where a
	// wrong argument changes the store rather than an answer.
	t.Run("a write with a required argument missing", func(t *testing.T) {
		refusedByTheSchema(t, "a commit with no message", "mnemo_commit", map[string]any{})
		refusedByTheSchema(t, "a project with no slug", "mnemo_upsert_project", map[string]any{})
		refusedByTheSchema(t, "a pending rewrite with no content", "mnemo_write_pending",
			map[string]any{"slug": "shop"})
		refusedByTheSchema(t, "a memory with no body", "mnemo_write_memory",
			map[string]any{"id": "x", "projects": []any{"shop"}, "type": "decision"})
	})
	t.Run("a write outside an enum", func(t *testing.T) {
		refusedByTheSchema(t, "a memory of no known type", "mnemo_write_memory",
			map[string]any{"id": "x", "projects": []any{"shop"}, "type": "musing", "body": "b"})
		refusedByTheSchema(t, "a project in no known state", "mnemo_upsert_project",
			map[string]any{"slug": "shop", "status": "hibernating"})
	})
	t.Run("a memory belonging to no project", func(t *testing.T) {
		refusedByTheSchema(t, "an empty projects list", "mnemo_write_memory",
			map[string]any{"id": "x", "projects": []any{}, "type": "decision", "body": "b"})
	})

	t.Run("what the schema allows gets through", func(t *testing.T) {
		result := call(t, session, "mnemo_load_project", map[string]any{"slug": "shop", "lang": "es", "detail": "card"})
		if result.IsError {
			t.Fatalf("a valid call was refused: %q", textOf(t, result))
		}
	})
}

// The order of the blocks is part of the contract, and it has to survive the
// trip through the protocol.
func TestTheBlocksArriveInOrder(t *testing.T) {
	session := connect(t, project(t, "## In progress\n\n- [ ] work\n", "A decision."))

	result := call(t, session, "mnemo_load_project", map[string]any{"slug": "shop"})
	if result.IsError {
		t.Fatalf("refused: %q", textOf(t, result))
	}
	got := textOf(t, result)
	if len(got) != 3 {
		t.Fatalf("got %d blocks: %q", len(got), got)
	}
	if !strings.HasPrefix(got[0], "shop — Shop") {
		t.Errorf("the first block is not the card:\n%s", got[0])
	}
	if !strings.HasPrefix(got[1], "Detail (do not print unless asked):") {
		t.Errorf("the second block is:\n%s", got[1])
	}
	if !strings.Contains(got[2], "This machine is 'here'") {
		t.Errorf("the third block is:\n%s", got[2])
	}
}

// A refusal is a result an agent can read and act on, not a protocol error that
// its client reports as a broken server.
func TestARefusalIsAResultNotAnError(t *testing.T) {
	session := connect(t, project(t, ""))

	result, err := session.CallTool(t.Context(),
		&mcp.CallToolParams{Name: "mnemo_read_memory", Arguments: map[string]any{"id": "not-there"}})
	if err != nil {
		t.Fatalf("a refusal came back as a protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("a missing memory was not reported as an error")
	}
	if got := textOf(t, result); len(got) != 1 || !strings.Contains(got[0], "mnemo_search_memories") {
		t.Errorf("the refusal reads %q; it should say what to do instead", got)
	}
}

// The rules reach a client that supports nothing but the base protocol.
func TestTheInstructionsTravelWithTheConnection(t *testing.T) {
	session := connect(t, project(t, ""))

	// The instructions are the only channel that reaches a client supporting
	// neither prompts nor resources, so they have to be on the handshake
	// itself, not merely defined somewhere.
	initialised := session.InitializeResult()
	if initialised == nil {
		t.Fatal("the client has no initialize result")
	}
	if initialised.Instructions != criterion.ServerInstructions {
		t.Errorf("the instructions that reached the client are:\n%s", initialised.Instructions)
	}
	if initialised.ServerInfo == nil || initialised.ServerInfo.Name != Name {
		t.Errorf("the server introduced itself as %+v", initialised.ServerInfo)
	}
	if initialised.ServerInfo.Version != "v0.0.0-test" {
		t.Errorf("the version reaching the client is %q", initialised.ServerInfo.Version)
	}

	// The guide is the other channel for the same rules, and it is a tool, so a
	// client with no prompts can still ask for them.
	result := call(t, session, "mnemo_guide", map[string]any{"target": "claude"})
	if result.IsError {
		t.Fatalf("refused: %q", textOf(t, result))
	}
	got := textOf(t, result)
	if !strings.Contains(got[0], "CLAUDE.md") {
		t.Errorf("the first block is %q", got[0])
	}
	for _, section := range []string{"## Saving", "## Machines", "## Confirmation", "## Mailbox"} {
		if !strings.Contains(got[1], section) {
			t.Errorf("the guide is missing %q", section)
		}
	}
}

// A handler that panics must refuse this one call, not end the chat's memory
// for the rest of the session.
func TestAPanicIsOneRefusalNotADeadServer(t *testing.T) {
	result := safely(t.Context(), func(_ context.Context, _ *Call, _ Args) *answer {
		panic("something impossible")
	}, nil, nil)

	if !result.IsError {
		t.Fatal("a panic came back as success")
	}
	text := textOf(t, result)[0]
	if !strings.HasPrefix(text, "mnemo failed unexpectedly: ") {
		t.Errorf("the result reads %q", text)
	}
	if !strings.Contains(text, "something impossible") {
		t.Errorf("the result does not say what happened: %q", text)
	}
}
