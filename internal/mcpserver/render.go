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
//
// Once an answer has failed it accepts nothing more. A refusal is one block by
// contract, and without this a handler that chains calls after a failure — the
// usual shape, `a.data(x).say(y)` — would send a refusal with three blocks, the
// last two describing a success that did not happen.
func (a *answer) say(format string, args ...any) *answer {
	if a.failed {
		return a
	}
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
	if a.failed {
		return a
	}
	if text != "" {
		a.blocks = append(a.blocks, text)
	}
	return a
}

// data appends a block of JSON.
func (a *answer) data(value any) *answer {
	if a.failed {
		return a
	}
	encoded, err := encode(value)
	if err != nil {
		// Encoding our own structs cannot fail for any reason the caller could
		// act on, so this becomes the unexpected-failure result rather than a
		// refusal that pretends to be advice. fail adds the prefix itself.
		return a.fail("%v", err)
	}
	return a.block(encoded)
}

// refuse turns the answer into a refusal: one block that says what to do
// instead. An agent reads isError and stops, so a refusal never carries the
// blocks of a successful answer alongside it.
func (a *answer) refuse(format string, args ...any) *answer {
	a.blocks = nil
	a.failed = true
	a.blocks = append(a.blocks, fmt.Sprintf(format, args...))
	return a
}

// fail is for what should not happen. It never looks like success, and it never
// looks like advice either: there is nothing for the agent to do differently.
func (a *answer) fail(format string, args ...any) *answer {
	a.blocks = nil
	a.failed = true
	a.blocks = append(a.blocks, "mnemo failed unexpectedly: "+fmt.Sprintf(format, args...))
	return a
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
