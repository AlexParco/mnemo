// Package httpapi is how one machine's mnemo becomes reachable from another: the
// HTTP interface a server answers on, behind a token, on loopback only.
//
// Nothing here is reachable from a network. The server listens on 127.0.0.1 and
// another machine arrives through an SSH tunnel, which is what makes the token
// the only secret that has to be carried: there is no port on the internet to
// find, and a page in a browser aimed at 127.0.0.1 does not have the token.
package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/AlexParco/mnemo/internal/build"
	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/mailbox"
	"github.com/AlexParco/mnemo/internal/mcpserver"
	"github.com/AlexParco/mnemo/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options are what a server answers with: the settings it was started from, and
// the store and mailbox it answers for.
//
// They are passed in rather than built here for the same reason the local server
// takes them: building a store means choosing where its lock lives, and that is
// a decision about the machine.
type Options struct {
	Settings config.Settings
	Store    *store.Store
	Mailbox  *mailbox.Mailbox
}

// Server is the HTTP interface. Build it with New and serve its Handler.
type Server struct {
	opts Options
	// digest is the SHA-256 of the real token, computed once. Every request is
	// compared against this rather than against the token itself, so that two
	// values of different lengths still take the same time to reject.
	digest [sha256.Size]byte
}

// ErrNoServer is what starting without a server section gives: a port with no
// token cannot answer a request, and a token with no port has nothing to listen
// on, so neither half is enough.
var ErrNoServer = errors.New("this machine has no server configured, so there is nothing to serve. " +
	"Set one up with: mnemo server setup")

// New checks that there is a server to run and prepares it.
func New(opts Options) (*Server, error) {
	if !opts.Settings.Server.Set() {
		return nil, ErrNoServer
	}
	return &Server{opts: opts, digest: sha256.Sum256([]byte(opts.Settings.Server.Token))}, nil
}

// Address is where it listens: loopback and nothing else. There is no option to
// listen elsewhere, because the one thing protecting the store is that nothing
// can reach the port without first getting onto the machine.
func (s *Server) Address() string {
	return fmt.Sprintf("127.0.0.1:%d", s.opts.Settings.Server.Port)
}

// Handler is everything the server answers, token check included.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", s.mcp())
	mux.HandleFunc("/v1/health", only(http.MethodGet, s.health))
	mux.HandleFunc("/v1/mailbox/heartbeat", only(http.MethodPost, s.heartbeat))
	mux.HandleFunc("/v1/mailbox/release", only(http.MethodPost, s.release))
	mux.HandleFunc("/v1/mailbox/holder", only(http.MethodGet, s.holder))
	mux.HandleFunc("/v1/mailbox/unread", only(http.MethodGet, s.unread))
	// Anything else is a 404 with a body of the same shape as every other
	// error, rather than net/http's own page: a client parses these.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "no such path: "+r.URL.Path)
	})
	return s.authorised(mux)
}

// authorised is the token check, and it runs before anything else reads a body.
//
// An unauthenticated request must not be able to make the server allocate, parse
// or touch the store. It also must not be able to learn anything from how long
// the refusal took: both tokens are hashed and the hashes compared in constant
// time, so neither the length nor the content of the real one leaks.
func (s *Server) authorised(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := bearer(r.Header.Get("Authorization"))
		if !ok {
			unauthorised(w, "this server needs a bearer token")
			return
		}
		got := sha256.Sum256([]byte(presented))
		if subtle.ConstantTimeCompare(got[:], s.digest[:]) != 1 {
			unauthorised(w, "that token is not this server's")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearer is the token out of an Authorization header, and whether there was one.
func bearer(header string) (string, bool) {
	// The scheme is case-insensitive in the HTTP specification, and a client
	// that sends `bearer` is not wrong.
	const scheme = "bearer "
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(header[len(scheme):]), true
}

// mcp is the MCP endpoint: the same tools the local server offers, answered for
// whoever is asking.
//
// Stateless, with JSON responses, so there is no session to lose: a server that
// restarts does not break a chat that is open on another machine, and the client
// does not have to carry a session id it would have to renew. The SDK's
// protection against DNS rebinding stays on; the token covers the rest.
func (s *Server) mcp() http.Handler {
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		requester, err := requesterFrom(r)
		if err != nil {
			// Returning nil would make the SDK answer 400 with its own body.
			// The check happens in the wrapper below instead, so the body is
			// the same shape as every other error here.
			return nil
		}
		return mcpserver.New(build.Version, mcpserver.ForRequest(
			s.opts.Settings, s.opts.Store, s.opts.Mailbox, requester))
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GET and DELETE have no meaning for a stateless server, and the SDK
		// refuses them itself; the headers are what has to be checked here,
		// because a call answered for nobody would be answered for this machine.
		if r.Method == http.MethodPost {
			if _, err := requesterFrom(r); err != nil {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		handler.ServeHTTP(w, r)
	})
}

// requesterFrom reads who is asking out of the headers.
//
// The machine is required because it decides which pending items belong to the
// caller, and a missing one would silently make them this machine's. The tool is
// required because it is what a group address matches. An agent is optional —
// a chat opened without a name has memory and no mailbox — but an agent without
// an instance could not hold a name, so one without the other is a mistake worth
// reporting rather than guessing at.
func requesterFrom(r *http.Request) (mcpserver.Requester, error) {
	requester := mcpserver.Requester{
		Machine:  strings.TrimSpace(r.Header.Get("X-Mnemo-Machine")),
		Tool:     strings.TrimSpace(r.Header.Get("X-Mnemo-Tool")),
		Agent:    strings.TrimSpace(r.Header.Get("X-Mnemo-Agent")),
		Instance: strings.TrimSpace(r.Header.Get("X-Mnemo-Instance")),
	}
	switch {
	case requester.Machine == "":
		return requester, errors.New("X-Mnemo-Machine is required: without it mnemo cannot tell which " +
			"machine's work is which")
	case requester.Tool == "":
		return requester, errors.New("X-Mnemo-Tool is required: it is what a group address matches")
	case requester.Agent != "" && requester.Instance == "":
		return requester, errors.New("X-Mnemo-Instance is required with X-Mnemo-Agent: a name is held by " +
			"an instance, so an agent without one could not hold it")
	}
	return requester, nil
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	// The api number is the whole point of this endpoint: a client checks it
	// before it trusts anything else, because a server that speaks a different
	// one would answer in a shape the client cannot read.
	send(w, http.StatusOK, map[string]any{
		"version": build.Version,
		"api":     build.API,
		"machine": s.opts.Settings.Machine,
	})
}

// heartbeat claims a name for an instance on another machine, or renews it.
//
// A remote holder lives on a lease rather than on a process check: this machine
// cannot see the other one's processes, so the client says it is still there
// every fifteen seconds and the name frees itself forty-five seconds after it
// stops saying so.
func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var asked struct {
		Agent    string `json:"agent"`
		Machine  string `json:"machine"`
		Tool     string `json:"tool"`
		Instance string `json:"instance"`
		PID      int    `json:"pid"`
	}
	if !decode(w, r, &asked) {
		return
	}
	caller := mailbox.Caller{
		Name: asked.Agent, Machine: asked.Machine, Tool: asked.Tool,
		Instance: asked.Instance, Via: "remote", PID: asked.PID,
	}
	held, err := s.opts.Mailbox.Claim(r.Context(), caller)
	if err == nil {
		send(w, http.StatusOK, map[string]any{
			"address":       held.Address,
			"lease_seconds": int(mailbox.Lease.Seconds()),
		})
		return
	}
	if !mailbox.IsRefusal(err) {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A refused claim is not a failure of the request: the name is simply
	// somebody else's. The holder goes in the body so the client can say which
	// chat and since when, in the same words a local refusal uses.
	body := map[string]any{"error": err.Error()}
	if occupant, live, lookErr := s.opts.Mailbox.Occupied(r.Context(), caller.Address()); lookErr == nil && live {
		body["holder"] = map[string]any{"tool": occupant.Tool, "since": occupant.Since.UTC()}
	}
	send(w, http.StatusConflict, body)
}

func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	var asked struct {
		Instance string `json:"instance"`
	}
	if !decode(w, r, &asked) {
		return
	}
	// Releasing something that was never held is not an error: a client that
	// crashed and came back has nothing to give up, and saying so would make it
	// report a problem it cannot do anything about.
	if err := s.opts.Mailbox.ReleaseInstance(r.Context(), asked.Instance); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) holder(w http.ResponseWriter, r *http.Request) {
	address := strings.TrimSpace(r.URL.Query().Get("address"))
	if address == "" {
		fail(w, http.StatusBadRequest, "address is required")
		return
	}
	occupant, live, err := s.opts.Mailbox.Occupied(r.Context(), address)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !live {
		send(w, http.StatusOK, map[string]any{"held": false})
		return
	}
	send(w, http.StatusOK, map[string]any{
		"held": true, "tool": occupant.Tool, "since": occupant.Since.UTC(),
	})
}

// unread is what a watcher on another machine asks, which is why it is a read
// that records nobody: announcing is not delivering, and the chat that holds the
// name still reads its own post.
func (s *Server) unread(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	address := strings.TrimSpace(query.Get("address"))
	if address == "" {
		fail(w, http.StatusBadRequest, "address is required")
		return
	}
	after := 0
	if raw := strings.TrimSpace(query.Get("after")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			fail(w, http.StatusBadRequest, "after must be a whole number of messages, not "+raw)
			return
		}
		after = parsed
	}
	top, waiting, err := s.opts.Mailbox.Peek(address, after)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if waiting == nil {
		waiting = []mailbox.Message{}
	}
	send(w, http.StatusOK, map[string]any{"last_seq": top, "messages": waiting})
}

// only answers one method and says which, so a client that used the wrong one
// learns the right one from the response rather than from the documentation.
func only(method string, handle http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			fail(w, http.StatusMethodNotAllowed, r.Method+" is not allowed here; use "+method)
			return
		}
		handle(w, r)
	}
}

// decode reads a JSON body, bounded, and answers the caller when it cannot.
func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	// The same ceiling the MCP endpoint uses, for the same reason: a body nobody
	// asked for must not be able to make the server hold it in memory.
	r.Body = http.MaxBytesReader(w, r.Body, mcp.DefaultMaxRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(w, http.StatusRequestEntityTooLarge, "that body is too large")
			return false
		}
		fail(w, http.StatusBadRequest, "that body is not the JSON this expects: "+err.Error())
		return false
	}
	return true
}

func send(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Nothing can be done about a write that fails here: the status line has
	// gone and the client is the only one who could notice.
	_ = json.NewEncoder(w).Encode(body)
}

// fail is every error this interface returns, in one shape, because a client
// parses them.
func fail(w http.ResponseWriter, status int, message string) {
	send(w, status, map[string]string{"error": message})
}

func unauthorised(w http.ResponseWriter, message string) {
	// The header is what tells a client it is a token that is missing or wrong
	// rather than the path, which is the difference between retrying and
	// asking the user to connect again.
	w.Header().Set("WWW-Authenticate", `Bearer realm="mnemo"`)
	fail(w, http.StatusUnauthorized, message)
}
