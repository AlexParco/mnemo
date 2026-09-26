package mcpserver

import (
	"context"

	"github.com/AlexParco/mnemo/internal/criterion"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Name is what mnemo calls itself to a client.
const Name = "mnemo"

// New builds the server an agent talks to.
//
// The call it is given is fixed for the life of the server, which is right for
// stdio: one process, one machine, one store, started by the tool that opened
// the chat. A server answering for other machines builds a call per request
// instead and reaches the same handlers.
func New(version string, call *Call) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: Name, Version: version},
		// The instructions are sent on every connection and are the only channel
		// that reaches a client supporting neither prompts nor resources, which
		// is why they are kept short and why they are a rule text rather than a
		// string written here.
		&mcp.ServerOptions{Instructions: criterion.ServerInstructions},
	)
	Register(server, call)
	return server
}

// Register adds every tool to a server. It takes the definitions rather than
// building them, so the list a client is offered comes from one place.
func Register(server *mcp.Server, call *Call) {
	for _, definition := range memoryTools() {
		add(server, definition, call)
	}
}

// add wires one definition to its handler.
//
// The generic AddTool is what validates the arguments against the schema before
// the handler runs; the server's own AddTool does not, and a handler reading an
// argument the caller sent as the wrong type would be a crash rather than a
// refusal. The input type is a map because the schema is written out in full and
// is the contract: decoding into a Go struct would be a second declaration of
// the same fields, and the two would drift.
func add(server *mcp.Server, definition Definition, call *Call) {
	handle := definition.Handle
	mcp.AddTool(server, definition.Tool,
		func(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
			// Whatever a handler panics on is this program's fault, not the
			// caller's, and it must not take the whole server down: one bad call
			// would end the chat's memory for the rest of the session.
			result := safely(ctx, handle, call, Args(args))
			return result, nil, nil
		})
}

// safely runs a handler and turns a panic into a result that says so. It never
// looks like success, and it never looks like advice either: there is nothing
// the agent could do differently.
func safely(ctx context.Context, handle Handler, call *Call, args Args) (result *mcp.CallToolResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = (&answer{}).fail("%v", recovered).result()
		}
	}()
	return handle(ctx, call, args).result()
}

// Serve answers on the given transport until the client goes away. Over stdio
// that is the whole life of the process.
func Serve(ctx context.Context, version string, call *Call, transport mcp.Transport) error {
	return New(version, call).Run(ctx, transport)
}
