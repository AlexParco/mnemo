package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexParco/mnemo/internal/config"
)

// The on-demand path is tested without a server and without a tunnel: the health
// check is a seam, so what is being checked here is the waiting — how often, for
// how long, and what it does before it starts waiting.

// marker is a stand-in mnemo that records that it was started, so a test can say
// whether Reach started a keeper or left well alone.
func marker(t *testing.T) (program, started string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in mnemo is a shell script")
	}
	bin := t.TempDir()
	program, started = filepath.Join(bin, "mnemo"), filepath.Join(bin, "started")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + started + "'\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return program, started
}

// awaitStart waits for the stand-in mnemo to have run. Start does not wait for
// the process, so neither can the test read the file the moment it returns.
func awaitStart(t *testing.T, started string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if raw, err := os.ReadFile(started); err == nil {
			if got := strings.TrimSpace(string(raw)); got != "tunnel" {
				t.Errorf("it started mnemo with %q, want tunnel", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Reach did not start the keeper")
}

func neverStarted(t *testing.T, started string) {
	t.Helper()
	if _, err := os.Stat(started); err == nil {
		t.Error("Reach started a keeper when it had no reason to")
	}
}

// checks counts the health checks and answers them as the test says.
func checks(answers func(n int) error) (*int, func(context.Context, config.Remote) error) {
	var mu sync.Mutex
	count := 0
	return &count, func(context.Context, config.Remote) error {
		mu.Lock()
		defer mu.Unlock()
		count++
		return answers(count)
	}
}

// rechecks is the sleep seam for the on-demand path: it records every pause and
// moves the clock on, so the ten seconds Reach is allowed pass at once.
func rechecks(c *clock) (*[]time.Duration, func(context.Context, time.Duration) bool) {
	var mu sync.Mutex
	seen := []time.Duration{}
	return &seen, func(_ context.Context, pause time.Duration) bool {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, pause)
		c.advance(pause)
		return true
	}
}

func TestReachCostsOneCheckWhenTheServerIsThere(t *testing.T) {
	program, started := marker(t)
	count, healthy := checks(func(int) error { return nil })

	keeper := New(machine(t), Options{Remote: saved(connected()), Program: program, Healthy: healthy,
		Sleep: func(context.Context, time.Duration) bool {
			t.Error("Reach waited although the server had answered")
			return false
		}})
	if err := keeper.Reach(t.Context()); err != nil {
		t.Fatalf("Reach: %v", err)
	}
	if *count != 1 {
		t.Errorf("it asked %d times, want once", *count)
	}
	neverStarted(t, started)
}

func TestReachDoesNothingOnAMachineThatIsNotConnected(t *testing.T) {
	program, started := marker(t)
	count, healthy := checks(func(int) error { return errors.New("nothing answered") })
	remote := connected()
	remote.Connected = false

	keeper := New(machine(t), Options{Remote: saved(remote), Program: program, Healthy: healthy})
	if err := keeper.Reach(t.Context()); err != nil {
		t.Fatalf("Reach in local mode: %v", err)
	}
	if *count != 0 {
		t.Errorf("it checked a server it has no reason to think exists, %d times", *count)
	}
	neverStarted(t, started)
}

func TestReachStartsTheKeeperAndWaitsForTheForward(t *testing.T) {
	program, started := marker(t)
	// Down, then down, then up: ssh took a moment to connect, which is the normal
	// case this whole path exists for.
	count, healthy := checks(func(n int) error {
		if n < 4 {
			return errors.New("nothing answered on 127.0.0.1:7001")
		}
		return nil
	})
	clock := newClock(0)
	pauses, sleep := rechecks(clock)

	keeper := New(machine(t), Options{Remote: saved(connected()), Program: program,
		Healthy: healthy, Sleep: sleep, Now: clock.now})
	if err := keeper.Reach(t.Context()); err != nil {
		t.Fatalf("Reach: %v", err)
	}
	awaitStart(t, started)

	if *count != 4 {
		t.Errorf("it asked %d times, want four: one before starting the keeper and three after", *count)
	}
	for i, pause := range *pauses {
		if pause != RecheckEvery {
			t.Errorf("pause %d was %s, want %s", i+1, pause, RecheckEvery)
		}
	}
	if len(*pauses) != 3 {
		t.Errorf("it waited %v, want three waits", *pauses)
	}
}

func TestReachGivesUpAfterTenSeconds(t *testing.T) {
	program, started := marker(t)
	count, healthy := checks(func(int) error { return errors.New("nothing answered on 127.0.0.1:7001") })
	clock := newClock(0)
	pauses, sleep := rechecks(clock)

	keeper := New(machine(t), Options{Remote: saved(connected()), Program: program,
		Healthy: healthy, Sleep: sleep, Now: clock.now})
	err := keeper.Reach(t.Context())
	if err == nil {
		t.Fatal("Reach said the server was there")
	}
	awaitStart(t, started)

	// Ten seconds at a quarter of a second apart, and the first check that
	// happened before the keeper was started.
	if want := int(ReadyWait / RecheckEvery); *count != want+1 {
		t.Errorf("it asked %d times, want %d", *count, want+1)
	}
	if want := int(ReadyWait / RecheckEvery); len(*pauses) != want {
		t.Errorf("it waited %d times, want %d", len(*pauses), want)
	}
	for _, text := range []string{"vps", "me@vps", "nothing answered", "mnemo connect", keeper.LogFile()} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("the refusal does not mention %q: %s", text, err)
		}
	}
}

func TestReachStopsAtOnceWhenTheServerAnswersWithAnError(t *testing.T) {
	program, started := marker(t)
	count, healthy := checks(func(int) error { return &Answered{Status: http.StatusUnauthorized} })

	keeper := New(machine(t), Options{Remote: saved(connected()), Program: program, Healthy: healthy,
		Sleep: func(context.Context, time.Duration) bool {
			t.Error("Reach kept waiting although the server had answered")
			return false
		}})
	err := keeper.Reach(t.Context())

	var answered *Answered
	if !errors.As(err, &answered) || answered.Status != http.StatusUnauthorized {
		t.Fatalf("Reach: %v, want the server's own answer", err)
	}
	if !strings.Contains(err.Error(), "mnemo connect") {
		t.Errorf("the refusal does not say what to do: %s", err)
	}
	if *count != 1 {
		t.Errorf("it asked %d times, want once: restarting the tunnel cannot fix a token", *count)
	}
	// Nothing is restarted, because the tunnel is plainly up: something answered
	// on the forwarded port.
	neverStarted(t, started)
}

func TestTheHealthCheckAsksThroughTheForwardWithTheToken(t *testing.T) {
	var asked struct {
		path  string
		token string
	}
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.path, asked.token = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(status)
		fmt.Fprintln(w, `{"version":"0.5.0","api":1,"machine":"vps"}`)
	}))
	defer server.Close()

	// httptest listens on loopback, which is where a real forward comes out too,
	// so the check runs against it unchanged.
	remote := connected()
	remote.LocalPort = port(t, server.URL)

	if err := ask(t.Context(), remote); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if asked.path != "/v1/health" {
		t.Errorf("it asked for %q, want /v1/health", asked.path)
	}
	if asked.token != "Bearer a-token" {
		t.Errorf("it sent %q, want the saved token as a bearer token", asked.token)
	}

	// A server that answers anything at all is a server that can be reached, and
	// the status is what the caller is told.
	status = http.StatusUnauthorized
	var answered *Answered
	if err := ask(t.Context(), remote); !errors.As(err, &answered) || answered.Status != status {
		t.Errorf("ask: %v, want a 401 the caller can tell apart", err)
	}

	// Nothing listening, which is what a tunnel that is down looks like.
	server.Close()
	err := ask(t.Context(), remote)
	if err == nil || !strings.Contains(err.Error(), "nothing answered") {
		t.Errorf("ask with no server: %v", err)
	}
	if errors.As(err, &answered) {
		t.Error("a tunnel that is down must not be reported as the server answering")
	}
}

func port(t *testing.T, address string) int {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	number, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return number
}
