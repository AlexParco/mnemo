// Package remote is the half of a connection that runs on the machine without
// the files: the client that talks to a mnemo server over the tunnel, and the
// relay a coding tool starts instead of a local server.
//
// The one rule that shapes all of it: a connected machine never falls back to
// its own files. A client that quietly wrote to its own mailbox would split the
// conversation in two without anybody noticing, and the two halves would never
// be reconciled — so a call that cannot reach the server is a refusal that says
// so, and never an answer from somewhere else.
package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/AlexParco/mnemo/internal/build"
	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/mailbox"
)

// Timeout is how long any one request to the server is given.
//
// Longer than a wait, which the server holds for up to 55 seconds, and short
// enough that a dead tunnel is noticed within a tool call rather than at the
// end of one.
const Timeout = 70 * time.Second

// HealthTimeout is how long the check that the server is there is given. It is
// short because everything does it before anything else: a chat opening, a
// message being sent, a watcher looking.
const HealthTimeout = 2 * time.Second

// Client is one connected machine's way to its server.
type Client struct {
	// Connection is where the server is and what the token is. It is the
	// resolved config, so nothing here reads the config file itself.
	Connection config.Connection
	// Caller is who this machine says it is, on every request. The machine and
	// the agent are identification and not authorisation: whoever holds the
	// token is the user.
	Caller mailbox.Caller

	http *http.Client
}

// New builds a client for a connection.
func New(connection config.Connection, caller mailbox.Caller) *Client {
	return &Client{
		Connection: connection,
		Caller:     caller,
		http:       &http.Client{Timeout: Timeout},
	}
}

// Endpoint is where the MCP calls go. It is this end of the tunnel: the server's
// own port is on its loopback and unreachable from here.
func (c *Client) Endpoint() string {
	return "http://" + c.Connection.Address() + "/mcp"
}

// Health is the version and the api number the server answers with.
type Health struct {
	Version string `json:"version"`
	API     int    `json:"api"`
	Machine string `json:"machine"`
}

// Reachable checks that the server is there and speaks the same api.
//
// Every part that needs the server calls this first, which is why it is cheap
// and has its own short timeout. The api check is the reason it returns the
// health rather than only an error: two machines with different numbers cannot
// understand each other's answers, and the one that is older has to be told to
// update rather than left to fail in some other way later.
func (c *Client) Reachable(ctx context.Context) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, HealthTimeout)
	defer cancel()

	var health Health
	status, body, err := c.do(ctx, http.MethodGet, "/v1/health", nil)
	if err != nil {
		return health, err
	}
	if status == http.StatusUnauthorized {
		return health, fmt.Errorf("the server on %s did not accept this machine's token. It was probably "+
			"rotated: ask for a new pairing code and run mnemo connect <code>", c.Connection.Machine)
	}
	if status != http.StatusOK {
		return health, fmt.Errorf("the server on %s answered %d to a health check: %s",
			c.Connection.Machine, status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &health); err != nil {
		return health, fmt.Errorf("the server on %s answered something that is not a health check: %s",
			c.Connection.Machine, strings.TrimSpace(string(body)))
	}
	if health.API != build.API {
		return health, fmt.Errorf("this machine speaks api %d and the server on %s speaks api %d, so they "+
			"cannot understand each other. Run mnemo update on whichever of the two is older — this one is "+
			"mnemo %s", build.API, c.Connection.Machine, health.API, build.Version)
	}
	return health, nil
}

// Heartbeat claims the caller's name on the server, or renews it.
//
// A remote holder lives on a lease because the server cannot see this machine's
// processes. The refusal, when the name is somebody else's, is kept and shown in
// the identity line rather than stopping the chat: losing the mailbox is not
// losing the memory.
func (c *Client) Heartbeat(ctx context.Context) (string, error) {
	asked := map[string]any{
		"agent": c.Caller.Name, "machine": c.Caller.Machine, "tool": c.Caller.Tool,
		"instance": c.Caller.Instance, "pid": c.Caller.PID,
	}
	status, body, err := c.do(ctx, http.MethodPost, "/v1/mailbox/heartbeat", asked)
	if err != nil {
		return "", err
	}
	switch status {
	case http.StatusOK:
		return "", nil
	case http.StatusConflict:
		var refused struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &refused); err != nil || refused.Error == "" {
			return "", fmt.Errorf("the server refused the name %s and did not say why", c.Caller.Address())
		}
		// Handed back as the message, not as an error: the caller shows it to
		// the agent and carries on without a mailbox.
		return refused.Error, nil
	default:
		return "", fmt.Errorf("the server on %s answered %d to a heartbeat: %s",
			c.Connection.Machine, status, strings.TrimSpace(string(body)))
	}
}

// Release gives the name back. It is sent when a chat closes, with a short
// timeout: the chat is already going, and the lease would free the name anyway.
func (c *Client) Release(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, _, err := c.do(ctx, http.MethodPost, "/v1/mailbox/release",
		map[string]any{"instance": c.Caller.Instance})
	return err
}

// Holder says whether an address is held on the server, which is what
// `mnemo run` asks before it opens a chat on this machine.
type Holder struct {
	Held  bool      `json:"held"`
	Tool  string    `json:"tool"`
	Since time.Time `json:"since"`
}

// Describe is the holder in the words a refusal uses, so a person reads the
// same sentence whether the name is held here or on the server.
func (h Holder) Describe() string {
	return mailbox.Occupant{Tool: h.Tool, Via: "remote", Since: h.Since}.Describe()
}

// Holding asks who has an address.
func (c *Client) Holding(ctx context.Context, address string) (Holder, error) {
	var holder Holder
	status, body, err := c.do(ctx, http.MethodGet, "/v1/mailbox/holder?address="+address, nil)
	if err != nil {
		return holder, err
	}
	if status != http.StatusOK {
		return holder, fmt.Errorf("the server on %s answered %d: %s",
			c.Connection.Machine, status, strings.TrimSpace(string(body)))
	}
	return holder, json.Unmarshal(body, &holder)
}

// Waiting is what a watcher asks for: the top of the log and what is unread.
type Waiting struct {
	LastSeq  int               `json:"last_seq"`
	Messages []mailbox.Message `json:"messages"`
}

// Unread is the server's peek, for `mnemo watch` on a connected machine. It
// records nobody and marks nothing read, exactly as the local one does.
func (c *Client) Unread(ctx context.Context, address string, after int) (Waiting, error) {
	var waiting Waiting
	path := fmt.Sprintf("/v1/mailbox/unread?address=%s&after=%d", address, after)
	status, body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return waiting, err
	}
	if status != http.StatusOK {
		return waiting, fmt.Errorf("the server on %s answered %d: %s",
			c.Connection.Machine, status, strings.TrimSpace(string(body)))
	}
	return waiting, json.Unmarshal(body, &waiting)
}

// do is every request this client makes: the token, the caller's headers, and
// the answer read whole.
//
// The body is read into memory rather than streamed because nothing here is
// large — the largest is a mailbox read, which the server bounds — and because a
// caller that has to close a body is a caller that will one day forget.
func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var payload *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = strings.NewReader(string(encoded))
	}

	var request *http.Request
	var err error
	url := "http://" + c.Connection.Address() + path
	if payload != nil {
		request, err = http.NewRequestWithContext(ctx, method, url, payload)
	} else {
		request, err = http.NewRequestWithContext(ctx, method, url, nil)
	}
	if err != nil {
		return 0, nil, err
	}
	c.Headers(request.Header)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, unreachable(c.Connection.Machine, err)
	}
	defer response.Body.Close()
	said, err := readAll(response)
	if err != nil {
		// The request was sent and the server was answering, so whatever it was
		// asked to do may have happened. That is a different sentence from a
		// request that never left, because the caller has to do a different
		// thing about it.
		return response.StatusCode, nil, &NoAnswer{Machine: c.Connection.Machine, Reason: reason(err)}
	}
	return response.StatusCode, said, nil
}

// Headers are what every request to the server carries: the token, and who is
// asking.
//
// The agent is left out when this chat has no name, because a name is what the
// mailbox admits and a chat without one has memory and nothing else. The
// instance always goes with it: a name is held by an instance.
func (c *Client) Headers(header http.Header) {
	header.Set("Authorization", "Bearer "+c.Connection.Token)
	header.Set("X-Mnemo-Machine", c.Caller.Machine)
	header.Set("X-Mnemo-Tool", c.Caller.Tool)
	if c.Caller.Name != "" {
		header.Set("X-Mnemo-Agent", c.Caller.Name)
		header.Set("X-Mnemo-Instance", c.Caller.Instance)
	}
}

func readAll(response *http.Response) ([]byte, error) {
	// Bounded, because the thing answering may not be mnemo at all: a proxy or
	// a captive portal on the way can answer anything, and an answer read for
	// ever is a chat that never comes back.
	const most = 8 << 20
	return io.ReadAll(io.LimitReader(response.Body, most))
}

// unreachable is what a caller is told when the request never left: the tunnel
// is down, or the server is not running.
//
// It says that nothing was done, here or there, because that is the one thing
// the caller needs in order to decide whether to try again — and it is true
// precisely because the request was not sent.
func unreachable(machine string, err error) error {
	return &NotSent{Machine: machine, Reason: reason(err)}
}

// NotSent is a call that never reached the server.
type NotSent struct {
	Machine string
	Reason  string
}

func (e *NotSent) Error() string {
	return fmt.Sprintf("mnemo cannot reach the server on %s (%s). Nothing was done, here or there. "+
		"The tunnel is being restarted; try again in a moment. If it keeps failing, tell the user to run "+
		"mnemo connect.", e.Machine, e.Reason)
}

// NoAnswer is a call that was sent and whose answer never came. It is a
// different sentence from NotSent because the caller has to do a different
// thing: what was asked for may have happened.
type NoAnswer struct {
	Machine string
	Reason  string
}

func (e *NoAnswer) Error() string {
	return fmt.Sprintf("The connection to the server on %s dropped while it was handling this call (%s). "+
		"It may or may not have taken effect. Check with mnemo_status or mailbox_peers before trying again.",
		e.Machine, e.Reason)
}

// reason is the short form of a network failure, for a sentence a person reads.
func reason(err error) string {
	var dns *net.DNSError
	switch {
	case err == nil:
		return "no reason given"
	case errors.As(err, &dns):
		return "the name " + dns.Name + " does not resolve"
	case strings.Contains(err.Error(), "connection refused"):
		return "nothing is listening on the tunnel's port"
	case strings.Contains(err.Error(), "context deadline exceeded"):
		return "it did not answer in time"
	default:
		return trimURL(err.Error())
	}
}

// trimURL takes the URL out of a network error, because it carries the token's
// port and a path nobody reading the sentence needs.
func trimURL(said string) string {
	if at := strings.LastIndex(said, ": "); at > 0 {
		return said[at+2:]
	}
	return said
}
