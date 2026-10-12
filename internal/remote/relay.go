package remote

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/AlexParco/mnemo/internal/build"
	"github.com/AlexParco/mnemo/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The relay: what a coding tool starts on a machine that is connected to a
// server.
//
// It looks exactly like a local mnemo to the tool — same tools, same
// instructions, same prompts — and every call that touches data goes to the
// server. The list and the texts are answered here because they cost a round
// trip and cannot differ: a client refuses to work with a server whose api
// number is not its own, and the api number is what changes when they do.

// Heartbeat is how often a relay says its chat is still open. The server frees a
// name 45 seconds after the last one, so this is a third of that: two may be
// lost before a name is taken away from a chat that is still there.
const Heartbeat = 15 * time.Second

// Relay serves a tool on a connected machine.
type Relay struct {
	client *Client

	// session is the connection to the server, made on first use and dropped
	// whenever a call fails at the transport level: a session that has seen a
	// broken pipe will see it again, and a new one costs one request.
	mu      sync.Mutex
	session *mcp.ClientSession

	// refused is why this chat does not hold its name, when the server said so.
	// It is shown in the identity line, and it does not stop the chat: losing
	// the mailbox is not losing the memory.
	refused string

	// checked is when the tunnel was last prodded, so that a run of failures
	// does not start the keeper once per call.
	lastCheck time.Time
	// WakeTunnel brings the tunnel up. It is injected because the relay must not
	// depend on how that is done — and because a test has no tunnel.
	WakeTunnel func(context.Context)
}

// NewRelay builds a relay for a client.
func NewRelay(client *Client) *Relay {
	return &Relay{client: client}
}

// Refused is why the chat does not hold its name, or the empty string.
func (r *Relay) Refused() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refused
}

// Serve answers one tool over stdio until the tool goes away.
//
// The server it builds is this machine's own definitions with a handler that
// forwards. Nothing about the store or the mailbox is read here: this machine
// has files and they are not the ones in use.
func (r *Relay) Serve(ctx context.Context, transport mcp.Transport) error {
	server := mcp.NewServer(
		&mcp.Implementation{Name: mcpserver.Name, Version: build.Version},
		&mcp.ServerOptions{Instructions: mcpserver.Instructions()},
	)
	mcpserver.RegisterForwarding(server, r.Call)

	if r.client.Caller.Name != "" {
		// Claimed before the handshake is answered, for the same reason a local
		// serve claims then: a second chat with the same name must fail when
		// somebody opens it, not later when a reply has two places to go.
		if refusal, err := r.client.Heartbeat(ctx); err == nil && refusal != "" {
			r.mu.Lock()
			r.refused = refusal
			r.mu.Unlock()
		}
		beating, stop := context.WithCancel(ctx)
		defer stop()
		go r.beat(beating)
		defer func() {
			// The release is best effort and short: the chat is already closing,
			// and the lease frees the name anyway.
			_ = r.client.Release(context.WithoutCancel(ctx))
		}()
	}
	return server.Run(ctx, transport)
}

// beat keeps the name while the chat is open.
func (r *Relay) beat(ctx context.Context) {
	ticker := time.NewTicker(Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refusal, err := r.client.Heartbeat(ctx)
			if err != nil {
				// A heartbeat that cannot get through is the tunnel's problem,
				// and the next tool call will say so in words the agent can
				// act on. Saying it here would be saying it to nobody: stdout
				// belongs to the protocol.
				r.wake(ctx)
				continue
			}
			r.mu.Lock()
			r.refused = refusal
			r.mu.Unlock()
		}
	}
}

// Call forwards one tool call to the server and gives back what the server said.
//
// Unchanged: the result, its blocks, its structured content and its errors are
// the server's. A relay that reinterpreted an answer would be a second place
// where the contract lives, and the two would drift.
func (r *Relay) Call(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	session, err := r.connected(ctx)
	if err != nil {
		r.wake(ctx)
		return nil, err
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		// The session has seen a failure at the transport level and will see it
		// again; the next call makes a new one.
		r.drop(session)
		r.wake(ctx)
		return nil, r.failed(err)
	}
	return result, nil
}

// failed turns a transport failure into the sentence the agent reads.
//
// Which of the two it is matters more than the reason: a call that never left
// changed nothing anywhere, and a call that was sent may have taken effect. An
// agent told the wrong one either repeats something that already happened or
// gives up on something that did not.
func (r *Relay) failed(err error) error {
	var notSent *NotSent
	if errors.As(err, &notSent) {
		return notSent
	}
	var noAnswer *NoAnswer
	if errors.As(err, &noAnswer) {
		return noAnswer
	}
	// Everything else reached the transport, so the request was on its way.
	return &NoAnswer{Machine: r.client.Connection.Machine, Reason: reason(err)}
}

// connected is the session, made if there is not one.
func (r *Relay) connected(ctx context.Context) (*mcp.ClientSession, error) {
	r.mu.Lock()
	existing := r.session
	r.mu.Unlock()
	if existing != nil {
		return existing, nil
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "mnemo-relay", Version: build.Version}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: r.client.Endpoint(),
		// The standalone event stream is off: mnemo never sends a request from
		// the server to a client, so the only thing it would do is hold a
		// connection open through the tunnel for nothing.
		DisableStandaloneSSE: true,
		HTTPClient:           &http.Client{Timeout: Timeout, Transport: headers{client: r.client}},
	}, nil)
	if err != nil {
		return nil, &NotSent{Machine: r.client.Connection.Machine, Reason: reason(err)}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		// Another call got there first. Two sessions do no harm, but one of them
		// would be left open for the life of the chat.
		go session.Close()
		return r.session, nil
	}
	r.session = session
	return session, nil
}

func (r *Relay) drop(session *mcp.ClientSession) {
	r.mu.Lock()
	if r.session == session {
		r.session = nil
	}
	r.mu.Unlock()
	go session.Close()
}

// wake prods the tunnel, at most once every ten seconds.
//
// A run of failures is one problem, not twenty: starting the keeper per failed
// call would launch a process for each one, and they would all find the lock
// taken anyway.
func (r *Relay) wake(ctx context.Context) {
	if r.WakeTunnel == nil {
		return
	}
	r.mu.Lock()
	recently := time.Since(r.lastCheck) < 10*time.Second
	if !recently {
		r.lastCheck = time.Now()
	}
	r.mu.Unlock()
	if !recently {
		r.WakeTunnel(ctx)
	}
}

// Close drops the session, if there is one.
func (r *Relay) Close() error {
	r.mu.Lock()
	session := r.session
	r.session = nil
	r.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}

// headers puts the token and the caller on every request the MCP client makes.
//
// It is a transport rather than a per-request wrapper because the SDK makes its
// own requests — the handshake, the calls, the closing — and every one of them
// needs them.
type headers struct {
	client *Client
}

func (h headers) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	h.client.Headers(cloned.Header)
	response, err := http.DefaultTransport.RoundTrip(cloned)
	if err != nil {
		return nil, &NotSent{Machine: h.client.Connection.Machine, Reason: reason(err)}
	}
	return response, nil
}

// A relay is closed by whoever opened it.
var _ io.Closer = (*Relay)(nil)
