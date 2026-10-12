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
	// Client is what the MCP client called itself in the handshake, as a label.
	// It is used when nothing said which tool this is, and only to show somebody
	// which chat they are looking at: it is never recorded on an address, so a
	// group address can never match it. A label a client chose for itself must
	// not be able to make `@claude` reach a chat that is not Claude Code.
	Client string
	// ClaimRefusal is why this chat does not hold its mailbox name: another live
	// chat has it. It is carried here because the claim happens at startup,
	// before any tool is called, and the agent has to be told — otherwise it
	// sends under a name whose replies go somewhere else.
	ClaimRefusal string

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

// Requester is who a request says it is, which over HTTP is all the server has:
// the headers. They are identification and not authorisation — whoever holds the
// token is the user, and these say which of their machines and chats is asking.
type Requester struct {
	Machine  string
	Tool     string
	Agent    string
	Instance string
}

// ForRequest builds the context of one call answered on somebody else's behalf.
//
// The files are the server's and the caller is not: that is the whole difference
// between this and Local, and it is why a Call is per request here and built
// once there. A pending item stamped for a machine belongs to whoever is asking,
// so the machine comes from the request; the store's hub and its autopush come
// from wherever the store is.
func ForRequest(settings config.Settings, writable *store.Store, box *mailbox.Mailbox,
	requester Requester) *Call {
	return &Call{
		store:    writable,
		box:      box,
		StoreDir: settings.Paths.Store,
		Machine:  requester.Machine,
		Agent:    requester.Agent,
		Tool:     requester.Tool,
		Instance: requester.Instance,
		Lang:     memory.ResolveLang(settings.Lang),
		Remote:   settings.Remote,
		Autopush: settings.Autopush,
		// ServedBy names the machine hosting the store, which is this one. The
		// answer says it so that a person reading a status knows whose files
		// they are looking at.
		ServedBy: settings.Machine,
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

// label turns a name a client gave itself into something that can be shown:
// lower-case, with runs of anything else turned into a single dash.
func label(name string) string {
	var out strings.Builder
	dashed := false
	for _, letter := range strings.ToLower(name) {
		if (letter >= 'a' && letter <= 'z') || (letter >= '0' && letter <= '9') {
			out.WriteRune(letter)
			dashed = false
			continue
		}
		if !dashed && out.Len() > 0 {
			out.WriteByte('-')
			dashed = true
		}
	}
	return strings.Trim(out.String(), "-")
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
	// Nothing is said to a chat that does not hold its name. The post belongs to
	// the chat that does, and mailbox_read would refuse this one — so the line
	// would be an instruction to call a tool that cannot work, about messages
	// that are somebody else's.
	if c.ClaimRefusal != "" {
		return ""
	}
	_, unread, err := c.box.Peek(c.Caller().Address(), 0)
	if err != nil || len(unread) == 0 {
		return ""
	}
	return fmt.Sprintf("%d message(s) waiting for you, from %s. Read them with mailbox_read.",
		len(unread), mailbox.Names(mailbox.Senders(unread)))
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
