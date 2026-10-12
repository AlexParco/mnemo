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
	for _, definition := range allTools() {
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
		func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
			// Who is calling can only be known from the connection, and a Call
			// is built before there is one. A copy rather than a write: one Call
			// serves every request of a session, and over HTTP it serves one.
			call := withClient(call, req)
			// Whatever a handler panics on is this program's fault, not the
			// caller's, and it must not take the whole server down: one bad call
			// would end the chat's memory for the rest of the session.
			result, structured := safely(ctx, handle, call, Args(args))
			announcePost(result, definition.Tool.Name, call)
			return result, structured, nil
		})
}

// announcePost adds the waiting-messages line to a successful answer.
//
// Every tool carries it, not only the mailbox ones: a chat that just searched its
// memory is awake and reachable, and that is the moment to mention the post. It
// is the last block so that nothing else has to move, and it is left off a
// refusal, which is one block by contract, and off reading the mailbox, which has
// just delivered everything there was.
func announcePost(result *mcp.CallToolResult, tool string, call *Call) {
	if result == nil || result.IsError || call == nil || tool == "mailbox_read" || tool == "mailbox_wait" {
		return
	}
	if line := call.waiting(); line != "" {
		result.Content = append(result.Content, &mcp.TextContent{Text: line})
	}
}

// withClient fills in the label of the client that connected, when nothing said
// which tool this is. It is what makes an answer say "you are worker@laptop1
// (some-editor)" rather than naming no tool at all.
func withClient(call *Call, req *mcp.CallToolRequest) *Call {
	if call == nil || call.Tool != "" || req == nil || req.Session == nil {
		return call
	}
	params := req.Session.InitializeParams()
	if params == nil || params.ClientInfo == nil {
		return call
	}
	named := label(params.ClientInfo.Name)
	if named == "" || named == call.Client {
		return call
	}
	copied := *call
	copied.Client = named
	return &copied
}

// safely runs a handler and turns a panic into a result that says so. It never
// looks like success, and it never looks like advice either: there is nothing
// the agent could do differently.
func safely(ctx context.Context, handle Handler, call *Call, args Args) (
	result *mcp.CallToolResult, structured any) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result, structured = (&answer{}).fail("%v", recovered).result(), nil
		}
	}()
	answered := handle(ctx, call, args)
	return answered.result(), answered.structured
}

// Serve answers on the given transport until the client goes away. Over stdio
// that is the whole life of the process.
func Serve(ctx context.Context, version string, call *Call, transport mcp.Transport) error {
	return New(version, call).Run(ctx, transport)
}

// Instructions are what every connection is told, wherever it is answered.
//
// A relay answers the handshake itself — it costs a round trip and cannot
// differ, because a client refuses to work with a server whose api number is not
// its own — so the text has to be reachable without building a server.
func Instructions() string { return criterion.ServerInstructions }

// Forwarder answers one tool call somewhere else. It is given the name and the
// arguments as they arrived, and gives back what the other end said.
type Forwarder func(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error)

// RegisterForwarding offers every tool and sends each call through the forwarder.
//
// The definitions are this binary's own: the list, the descriptions and the
// schemas are the same on both sides by construction, which is what the api
// number is for. What travels is the call and the answer, and the answer is
// handed back exactly as it came — a relay that reinterpreted a result would be
// a second place where the contract lives, and the two would drift.
func RegisterForwarding(server *mcp.Server, forward Forwarder) {
	for _, definition := range allTools() {
		tool := definition.Tool
		mcp.AddTool(server, tool,
			func(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
				result, err := forward(ctx, tool.Name, args)
				if err != nil {
					// The failure is the agent's to act on — whether the call
					// was sent decides what it should do next — so it is the
					// answer rather than a protocol error, which a client would
					// show as mnemo being broken.
					return (&answer{}).refuse("%s", err.Error()).result(), nil, nil
				}
				return result, nil, nil
			})
	}
}
