package service

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Installing a service says nothing about whether the server came up. systemctl
// and launchctl both succeed as soon as they have handed the process to the
// supervisor, so the only way to know there is a server listening is to ask it —
// with the token, because that is the only thing it answers.

// HealthTimeout is how long setup waits for the server to answer.
const HealthTimeout = 10 * time.Second

// healthInterval is how often it asks in the meantime. The same quarter second
// the on-demand tunnel check uses.
const healthInterval = 250 * time.Millisecond

// WaitHealthy waits for the service's health endpoint to answer with the token,
// and reports what to read when it does not.
//
// The port and the token are passed in rather than read here: they belong to the
// config, which is written before the service is installed, and this package
// deliberately never holds the token for longer than one request.
func (m *Manager) WaitHealthy(port int, token string, within time.Duration) Result {
	address := "127.0.0.1:" + strconv.Itoa(port)
	result := Result{Detail: address}
	request := func() (int, error) {
		req, err := http.NewRequest(http.MethodGet, "http://"+address+"/v1/health", nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		// One request's timeout, not the whole wait's: a server that accepts the
		// connection and then stalls must not swallow the time left for the
		// rest of the attempts.
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		response.Body.Close()
		return response.StatusCode, nil
	}

	deadline := time.Now().Add(within)
	var reason string
	for {
		status, err := request()
		switch {
		case err != nil:
			reason = err.Error()
		case status == http.StatusOK:
			result.Outcome = Running
			return result
		case status == http.StatusUnauthorized:
			// Waiting would not help: something is listening on the port and it
			// does not accept this token, which is what a server left running
			// with an older one looks like.
			result.Outcome, result.Detail = Refused, "the server on "+address+" rejected the token"
			result.Advice = "Either another mnemo server is already running there with a different token, or this " +
				"one is still using the token it started with.\nRun: mnemo server restart"
			return result
		default:
			reason = "it answered " + strconv.Itoa(status)
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(healthInterval)
	}

	result.Outcome = Refused
	result.Detail = fmt.Sprintf("the server did not answer on %s within %s", address, within)
	result.Advice = "The service is installed, so this is the server failing to start or failing to listen (" +
		reason + ").\nRead why with: " + m.logCommand()
	return result
}

// logCommand is where this platform keeps what the service printed as it failed.
func (m *Manager) logCommand() string {
	switch m.Platform {
	case MacOS:
		return "cat " + m.LogFile()
	case Windows:
		return "mnemo serve --http"
	default:
		return "journalctl --user -u " + UnitName + " -n 50"
	}
}
