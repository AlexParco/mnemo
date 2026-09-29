package mcpserver

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/gitx"
	"github.com/AlexParco/mnemo/internal/mailbox"
	"github.com/AlexParco/mnemo/internal/memory"
	"github.com/AlexParco/mnemo/internal/store"
)

// Call is the context one tool call runs in: whose files, which machine, which
// agent asked.
//
// Locally it is built once when `mnemo serve` starts, from the settings. Over
// HTTP the server builds one per request from the caller's headers, because the
// files are the server's while the machine and the agent are the caller's. That
// is the whole difference between the two modes, and keeping it in one value is
// what lets the handlers be unaware of which mode they are in.
type Call struct {
	// StoreDir is the store these files belong to.
	StoreDir string
	// Machine is the caller's machine label, which is the one used to decide
	// whether a pending item stamped for a machine belongs to whoever is asking.
	Machine string
	Agent   string
	Tool    string
	Lang    memory.Lang
	// Remote and Autopush belong to the store, so they come from wherever the
	// store is, not from the caller.
	Remote   string
	Autopush bool
	// ServedBy names the machine hosting the store when the call came over HTTP,
	// and is empty for a local call.
	ServedBy string
	// Instance is this process, which is what holds a mailbox name.
	Instance string

	// box is this machine's mailbox, or nil for a connection that has none. It is
	// here so that every answer can carry a line about waiting messages: an agent
	// that just searched its memory has told mnemo it is awake, and that is the
	// cheapest moment there will ever be to mention the post.
	box *mailbox.Mailbox
	// store is what the write tools act through. It is built by whoever builds
	// the call — the command locally, the server for a request — because
	// building one means choosing where the lock lives, and that is a decision
	// about the machine rather than about the tools.
	store *store.Store
}

// Local builds the context of a call answered by this machine on its own files.
func Local(settings config.Settings, writable *store.Store, box *mailbox.Mailbox) *Call {
	return &Call{
		store:    writable,
		box:      box,
		StoreDir: settings.Paths.Store,
		Machine:  settings.Machine,
		Agent:    settings.Agent,
		Tool:     settings.Tool,
		Lang:     memory.ResolveLang(settings.Lang),
		Remote:   settings.Remote,
		Autopush: settings.Autopush,
		Instance: mailbox.Instance(),
	}
}

// writable is the store, or a refusal for a caller that has none. A read-only
// context is a real state — the relay builds one for a client that may not
// write — and the tools have to say so rather than crash.
func (c *Call) writable() (*store.Store, error) {
	if c.store == nil {
		return nil, errNotWritable
	}
	if c.store.Dir == "" {
		return nil, errNowhere
	}
	return c.store, nil
}

// Caller is who this is, in the mailbox's terms.
func (c *Call) Caller() mailbox.Caller {
	return mailbox.Caller{
		Name: c.Agent, Machine: c.Machine, Tool: c.Tool,
		Instance: c.Instance, Via: "local", PID: os.Getpid(),
	}
}

// waiting is the line every answer ends on when this chat has post, and the
// empty string when it has none.
//
// It reads with peek, which takes no lock and marks nothing: adding a line to an
// answer must never be able to block another agent, and must never consume the
// message it is announcing.
func (c *Call) waiting() string {
	if c.box == nil || c.Agent == "" || c.Machine == "" {
		return ""
	}
	_, unread, err := c.box.Peek(c.Caller().Address(), 0)
	if err != nil || len(unread) == 0 {
		return ""
	}
	senders := map[string]bool{}
	var order []string
	for _, message := range unread {
		if !senders[message.From] {
			senders[message.From] = true
			order = append(order, message.From)
		}
	}
	return fmt.Sprintf("%d message(s) waiting for you, from %s. Read them with mailbox_read.",
		len(unread), englishList(order))
}

// englishList joins names the way a person reads them.
func englishList(values []string) string {
	switch len(values) {
	case 0:
		return "nobody"
	case 1:
		return values[0]
	case 2:
		return values[0] + " and " + values[1]
	default:
		return strings.Join(values[:len(values)-1], ", ") + " and " + values[len(values)-1]
	}
}

// repo answers git questions about the store.
func (c *Call) repo() *gitx.Repo { return gitx.New(c.StoreDir) }

// storeExists reports whether there is anything there at all. Every read tool
// asks first, because "no store yet" is a different answer from "nothing
// matched", and an agent that confuses them tells the user their memory is empty.
func (c *Call) storeExists() bool {
	info, err := os.Stat(c.StoreDir)
	return err == nil && info.IsDir()
}

// language is the language for this call: what the caller asked for, else the
// one the settings resolved.
func (c *Call) language(requested string) memory.Lang {
	if requested != "" {
		return memory.ResolveLang(requested)
	}
	if c.Lang != "" {
		return c.Lang
	}
	return memory.EN
}

// Args are one call's arguments, already validated against the tool's schema.
//
// Reading them back out of a map is deliberate: the schema is the contract and
// is written out in full, so decoding into a Go struct would be a second
// declaration of the same thing, and the two would drift. What the schema
// guarantees is that a field that is present has the type it says, which is why
// these accessors treat a wrong type as absent rather than reporting it.
type Args map[string]any

func (a Args) str(name string) string {
	value, _ := a[name].(string)
	return value
}

// num reads an integer. JSON has one number type, so a value that arrived over
// the wire is a float64 or a json.Number depending on the decoder.
func (a Args) num(name string, fallback int) int {
	switch value := a[name].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case json.Number:
		if n, err := value.Int64(); err == nil {
			return int(n)
		}
	}
	return fallback
}

// has reports whether the caller sent this argument at all, which is not the
// same as sending it empty: a description set to "" clears it, and a description
// left out leaves it alone.
func (a Args) has(name string) bool {
	_, given := a[name]
	return given
}

func (a Args) flag(name string) bool {
	value, _ := a[name].(bool)
	return value
}

// list reads an array of strings. Absent comes back nil and empty comes back
// empty, because the store tells those apart: nil leaves a field alone, an
// empty list clears it.
func (a Args) list(name string) []string {
	raw, ok := a[name].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// choice reads a string that the schema constrains to a small set, falling back
// when it is absent.
func (a Args) choice(name, fallback string) string {
	if value := a.str(name); value != "" {
		return value
	}
	return fallback
}
