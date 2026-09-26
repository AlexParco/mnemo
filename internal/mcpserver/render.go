// Package mcpserver is the MCP surface: the tool definitions an agent sees and
// the handlers behind them.
//
// The definitions are data, in one table, because the same list has to be served
// by this process over stdio and by a server over HTTP on behalf of a machine
// that is not this one. Two lists would eventually disagree, and an agent would
// see a tool that does not exist or miss one that does.
package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A result is a list of text blocks whose order is part of the contract: the
// first block of mnemo_load_project is the card, and an agent is told to print
// that one and nothing else. Anything that reorders them breaks that promise, so
// blocks are only ever appended.
type answer struct {
	blocks []string
	failed bool
}

// say appends one block, formatted.
func (a *answer) say(format string, args ...any) *answer {
	if len(args) == 0 {
		a.blocks = append(a.blocks, format)
		return a
	}
	a.blocks = append(a.blocks, fmt.Sprintf(format, args...))
	return a
}

// block appends a block already built, skipping an empty one so a caller can
// add a section conditionally without leaving a hole.
func (a *answer) block(text string) *answer {
	if text != "" {
		a.blocks = append(a.blocks, text)
	}
	return a
}

// data appends a block of JSON.
func (a *answer) data(value any) *answer {
	encoded, err := encode(value)
	if err != nil {
		// Encoding our own structs cannot fail for any reason the caller could
		// act on, so this becomes the unexpected-failure result rather than a
		// refusal that pretends to be advice.
		return a.fail("mnemo failed unexpectedly: %v", err)
	}
	return a.block(encoded)
}

// refuse turns the answer into a refusal: one block that says what to do
// instead. An agent reads isError and stops, so a refusal never carries the
// blocks of a successful answer alongside it.
func (a *answer) refuse(format string, args ...any) *answer {
	a.blocks = nil
	a.failed = true
	return a.say(format, args...)
}

// fail is for what should not happen. It never looks like success, and it never
// looks like advice either: there is nothing for the agent to do differently.
func (a *answer) fail(format string, args ...any) *answer {
	a.blocks = nil
	a.failed = true
	return a.say("mnemo failed unexpectedly: "+format, args...)
}

// result is what the SDK sends.
func (a *answer) result() *mcp.CallToolResult {
	content := make([]mcp.Content, 0, len(a.blocks))
	for _, text := range a.blocks {
		content = append(content, &mcp.TextContent{Text: text})
	}
	return &mcp.CallToolResult{Content: content, IsError: a.failed}
}

// encode writes JSON the way the contract describes it: two-space indentation,
// field order as declared by the struct, and `<`, `>` and `&` left alone.
//
// The standard encoder escapes those three into `<` and friends, which is
// protection against JSON being pasted into an HTML page. Nothing here goes near
// HTML, and a memory about a shell pipeline or a comparison would come back
// unreadable to the agent that has to act on it.
func encode(value any) (string, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	// Encode adds a newline that a text block does not want.
	return string(bytes.TrimRight(out.Bytes(), "\n")), nil
}
