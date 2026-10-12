package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/AlexParco/mnemo/internal/config"
)

// Answered is what a health check returns when the server replied with
// something other than 200.
//
// It is a type of its own because the tunnel is plainly up — something answered
// on the forwarded port — so Reach must stop instead of restarting ssh. The
// usual cause is a rotated token, and no amount of reconnecting fixes that.
type Answered struct {
	Status int
}

func (a *Answered) Error() string {
	if a.Status == http.StatusUnauthorized {
		return "the server refused this machine's token (401), which usually means it was rotated. " +
			"Get a new pairing code with mnemo server rotate on the server, and paste it here with mnemo connect <code>"
	}
	return fmt.Sprintf("the server answered %d instead of 200", a.Status)
}

// Reach makes sure the server can be reached, and brings the tunnel up when it
// cannot. Every part that needs the server calls it first.
//
// On a machine that is not connected it does nothing and says so by returning
// nil: local mode has no server to reach, and this is called from code that does
// not want to know which mode it is in.
//
// The happy path is one health check, which is why the check has a short timeout:
// it is paid by every chat that opens, every `watch` tick that follows a failure
// and every terminal command. Only when that fails does anything get started.
func (k *Keeper) Reach(ctx context.Context) error {
	remote, saved, err := k.remote()
	if err != nil {
		return err
	}
	if !saved || !remote.Connected {
		return nil
	}

	failure := k.healthy(ctx, remote)
	if failure == nil {
		return nil
	}
	var answered *Answered
	if errors.As(failure, &answered) {
		return answered
	}

	if err := k.Start(); err != nil {
		return err
	}
	// Asked again rather than waited on: there is no event to watch for. ssh has
	// to connect, authenticate and open the forward, and the only thing that says
	// it finished is the server answering through it.
	deadline := k.now().Add(ReadyWait)
	for k.now().Before(deadline) {
		if !k.sleep(ctx, RecheckEvery) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			break
		}
		failure = k.healthy(ctx, remote)
		if failure == nil {
			return nil
		}
		if errors.As(failure, &answered) {
			return answered
		}
	}
	// The log is named because ssh's own words are in it, and they are what
	// distinguishes a rejected key from a machine that is simply off.
	return fmt.Errorf("mnemo cannot reach the server on %s through %s, %s after starting the tunnel (%w). "+
		"Check %s, and run mnemo connect if it keeps failing",
		remote.Machine, remote.SSH, ReadyWait, failure, k.LogFile())
}

// probe is the client the health check uses.
//
// It has no proxy. The request goes to this machine's own loopback port, and a
// proxy picked up from the environment — which a person sets for their browser,
// not for a tunnel — would send a check for a local port to somebody else's
// server, and the answer would mean nothing.
// Its own timeout backs up the context's, so a server that accepts the
// connection and then says nothing still fails fast.
var probe = &http.Client{
	Transport: &http.Transport{Proxy: nil},
	Timeout:   HealthTimeout + time.Second,
}

// ask is the real health check: `GET /v1/health` through the forward.
//
// It only asks whether the server is there. Whether its API version is one this
// client can work with is the relay's question, asked once when a session opens,
// and asking it here would mean refusing to bring a tunnel up over a disagreement
// the tunnel cannot cause.
func ask(ctx context.Context, remote config.Remote) error {
	ctx, cancel := context.WithTimeout(ctx, HealthTimeout)
	defer cancel()

	address := "127.0.0.1:" + strconv.Itoa(remote.LocalPort)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/v1/health", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+remote.Token)
	response, err := probe.Do(request)
	if err != nil {
		// Unwrapped: the useful part is "connection refused", and the URL that
		// net/http wraps it with repeats the address the caller is told anyway.
		return fmt.Errorf("nothing answered on %s", address)
	}
	defer response.Body.Close()
	// Drained so the connection can be reused by the next check, which happens
	// every couple of seconds on a machine that is working.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return &Answered{Status: response.StatusCode}
	}
	return nil
}
