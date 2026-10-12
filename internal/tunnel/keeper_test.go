package tunnel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/lock"
)

// Nothing in this package's tests may reach a host, start a server or write
// outside a temp directory. Three seams make that possible, and each of them is
// here rather than in the package because they are the only things a test needs
// that production does not: a fake ssh on the PATH, a clock the test moves, and
// a sleep that ends the loop instead of waiting.

// machine is one machine's state directory, in a temp directory of its own.
func machine(t *testing.T) config.Paths {
	t.Helper()
	state := t.TempDir()
	return config.Paths{
		State:   state,
		Locks:   filepath.Join(state, "locks"),
		Tunnel:  filepath.Join(state, "tunnel"),
		Logs:    filepath.Join(state, "logs"),
		Mailbox: filepath.Join(state, "mailbox"),
	}
}

// connected is a saved remote for a server the tests never talk to.
func connected() config.Remote {
	return config.Remote{
		Machine: "vps", SSH: "me@vps",
		ServerPort: 7433, LocalPort: 7001,
		Token: "a-token", Connected: true,
	}
}

func saved(remote config.Remote) func() (config.Remote, bool, error) {
	return func() (config.Remote, bool, error) { return remote, true, nil }
}

// fakeSSH puts a script called ssh at the front of the PATH, and returns the
// file it appends its arguments to.
//
// ssh is the seam because ssh is the thing the keeper exists to run: a test that
// mocked one layer above it would not check the command line, which is the part
// of the specification most likely to be got wrong.
func fakeSSH(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a shell script")
	}
	bin := t.TempDir()
	recorded := filepath.Join(bin, "arguments")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + recorded + "'\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return recorded
}

// ranWith is every command line the fake ssh was given, in order.
func ranWith(t *testing.T, recorded string) []string {
	t.Helper()
	raw, err := os.ReadFile(recorded)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// clock is the keeper's time, under the test's control.
//
// step is added on every reading, which is how a test says how long ssh appeared
// to run for: the loop measures a run by reading the clock on either side of it.
// advance is for the sleep seam, so that fake time moves only where the keeper
// would really have waited.
type clock struct {
	mu   sync.Mutex
	at   time.Time
	step time.Duration
}

func newClock(step time.Duration) *clock {
	return &clock{at: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), step: step}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(c.step)
	return c.at
}

func (c *clock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(by)
}

// restarts is the sleep seam for the loop: it records every wait, moves the
// clock on by it, and ends the loop after the given number of them, which is how
// a test sees the whole backoff sequence in no time at all.
func restarts(c *clock, stopAfter int) (*[]time.Duration, func(context.Context, time.Duration) bool) {
	var mu sync.Mutex
	seen := []time.Duration{}
	return &seen, func(_ context.Context, pause time.Duration) bool {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, pause)
		c.advance(pause)
		return len(seen) < stopAfter
	}
}

func TestTheKeeperRunsSSHWithTheCommandLineTheSpecificationRequires(t *testing.T) {
	paths := machine(t)
	recorded := fakeSSH(t, "exit 255")
	clock := newClock(0)
	_, sleep := restarts(clock, 1)

	keeper := New(paths, Options{Remote: saved(connected()), Sleep: sleep, Now: clock.now})
	if err := keeper.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := "-N -o BatchMode=yes -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 " +
		"-o ServerAliveCountMax=3 -L 127.0.0.1:7001:127.0.0.1:7433 me@vps"
	ran := ranWith(t, recorded)
	if len(ran) != 1 || ran[0] != want {
		t.Errorf("ssh ran as:\n got: %q\nwant: %q", ran, []string{want})
	}
}

func TestTheWaitsBetweenRestartsGrowAndStayAtThirtySeconds(t *testing.T) {
	paths := machine(t)
	fakeSSH(t, "exit 255")
	// A clock that does not move: every run of ssh lasted no time at all, which
	// is what a connection that keeps failing looks like.
	clock := newClock(0)
	seen, sleep := restarts(clock, 8)

	keeper := New(paths, Options{Remote: saved(connected()), Sleep: sleep, Now: clock.now})
	if err := keeper.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	if len(*seen) != len(want) {
		t.Fatalf("waited %v", *seen)
	}
	for i, wanted := range want {
		if (*seen)[i] != wanted*time.Second {
			t.Errorf("wait %d was %s, want %s", i+1, (*seen)[i], wanted*time.Second)
		}
	}
}

func TestARunThatLastedAMinuteResetsTheWaits(t *testing.T) {
	// The two sides of the minute. A run that reached it was a connection that
	// worked, and the next failure starts over; a run that nearly reached it was
	// a failure that took a while to happen, and the backoff has to keep
	// climbing or a tunnel that dies after fifty-nine seconds is retried for
	// ever at a second apart.
	cases := []struct {
		name  string
		ssh   time.Duration
		waits []time.Duration
	}{
		{name: "a run of a minute and a second", ssh: 61 * time.Second,
			waits: []time.Duration{time.Second, time.Second, time.Second, time.Second}},
		{name: "a run of fifty-nine seconds", ssh: 59 * time.Second,
			waits: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			paths := machine(t)
			fakeSSH(t, "exit 255")
			// The clock jumps by this much on every reading, and the loop reads it
			// on either side of ssh, so this is how long each run appeared to last.
			clock := newClock(c.ssh)
			seen, sleep := restarts(clock, len(c.waits))

			keeper := New(paths, Options{Remote: saved(connected()), Sleep: sleep, Now: clock.now})
			if err := keeper.Run(t.Context()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(*seen) != len(c.waits) {
				t.Fatalf("waited %v, want %v", *seen, c.waits)
			}
			for i, want := range c.waits {
				if (*seen)[i] != want {
					t.Errorf("wait %d was %s, want %s", i+1, (*seen)[i], want)
				}
			}
		})
	}
}

func TestASecondKeeperExitsAtOnceAndSuccessfully(t *testing.T) {
	paths := machine(t)
	recorded := fakeSSH(t, "exit 255")

	// The lock is taken the way another keeper would take it, from a locker of
	// its own: the operating system is what makes it exclusive, not this process.
	held, err := lock.New(paths.Tunnel).Acquire(t.Context(), paths.Tunnel)
	if err != nil {
		t.Fatalf("taking the lock first: %v", err)
	}
	defer held()

	clock := newClock(0)
	_, sleep := restarts(clock, 1)
	keeper := New(paths, Options{Remote: saved(connected()), Sleep: sleep, Now: clock.now})

	started := time.Now()
	if err := keeper.Run(t.Context()); err != nil {
		t.Fatalf("a second keeper must exit successfully, got: %v", err)
	}
	if waited := time.Since(started); waited > 2*time.Second {
		t.Errorf("the second keeper took %s to exit; it must give up at once", waited)
	}
	if ran := ranWith(t, recorded); len(ran) != 0 {
		t.Errorf("the second keeper ran ssh: %v", ran)
	}
}

func TestTheKeeperWritesItsProcessIDAndRemovesItWhenItStops(t *testing.T) {
	paths := machine(t)
	fakeSSH(t, "exit 255")
	clock := newClock(0)

	running := ""
	keeper := New(paths, Options{
		Remote: saved(connected()),
		Now:    clock.now,
		// Read from inside the loop, because the file is only there while the
		// keeper is: after Run returns there is nothing left to look at.
		Sleep: func(context.Context, time.Duration) bool {
			raw, err := os.ReadFile(filepath.Join(paths.Tunnel, "keeper.pid"))
			if err != nil {
				t.Errorf("reading the process id file: %v", err)
			}
			running = strings.TrimSpace(string(raw))
			return false
		},
	})
	if err := keeper.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if running != strconv.Itoa(os.Getpid()) {
		t.Errorf("the process id file said %q, want %d", running, os.Getpid())
	}
	if _, err := os.Stat(filepath.Join(paths.Tunnel, "keeper.pid")); !os.IsNotExist(err) {
		t.Errorf("the process id file outlived the keeper: %v", err)
	}
}

func TestTheKeeperExitsWhenTheMachineIsNotConnected(t *testing.T) {
	cases := []struct {
		name   string
		remote func() (config.Remote, bool, error)
	}{
		{
			name:   "with no remote saved at all",
			remote: func() (config.Remote, bool, error) { return config.Remote{}, false, nil },
		},
		{
			name: "with a remote that disconnect turned off",
			remote: func() (config.Remote, bool, error) {
				remote := connected()
				remote.Connected = false
				return remote, true, nil
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			paths := machine(t)
			recorded := fakeSSH(t, "exit 255")
			keeper := New(paths, Options{
				Remote: c.remote,
				Sleep: func(context.Context, time.Duration) bool {
					t.Error("it waited to restart ssh instead of exiting")
					return false
				},
			})
			if err := keeper.Run(t.Context()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if ran := ranWith(t, recorded); len(ran) != 0 {
				t.Errorf("it ran ssh: %v", ran)
			}
			if _, err := os.Stat(filepath.Join(paths.Tunnel, "keeper.pid")); !os.IsNotExist(err) {
				t.Errorf("the process id file outlived the keeper: %v", err)
			}
		})
	}
}

func TestWhatSSHSaysGoesToTheLog(t *testing.T) {
	paths := machine(t)
	fakeSSH(t, "echo 'Permission denied (publickey).' >&2\nexit 255")
	clock := newClock(0)
	_, sleep := restarts(clock, 1)

	keeper := New(paths, Options{Remote: saved(connected()), Sleep: sleep, Now: clock.now})
	if err := keeper.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	written := read(t, keeper.LogFile())
	for _, want := range []string{
		"Permission denied (publickey).",
		"forwarding 127.0.0.1:7001 to 127.0.0.1:7433 through me@vps",
		"starting it again in 1s",
	} {
		if !strings.Contains(written, want) {
			t.Errorf("the log does not mention %q:\n%s", want, written)
		}
	}
}

func TestTheLogIsRolledOverWhenItPassesAMebibyte(t *testing.T) {
	paths := machine(t)
	fakeSSH(t, "exit 255")
	clock := newClock(0)
	_, sleep := restarts(clock, 1)
	keeper := New(paths, Options{Remote: saved(connected()), Sleep: sleep, Now: clock.now})

	// A log that is already full, and an earlier rolled-over one that must not
	// survive: two files is the whole of the policy.
	if err := os.MkdirAll(paths.Logs, 0o700); err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("x", maxLog)
	if err := os.WriteFile(keeper.LogFile(), []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keeper.LogFile()+".1", []byte("older than the one before"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := keeper.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The full log, and the line that took it over the limit, are what was rolled
	// over: the line is written whole and then the file is moved aside, so a
	// person reading it never finds half a sentence at the end.
	rolled := read(t, keeper.LogFile()+".1")
	if !strings.HasPrefix(rolled, full) || !strings.Contains(rolled, "forwarding") {
		t.Errorf("tunnel.log.1 is not the full log that was rolled over (%d bytes)", len(rolled))
	}
	if strings.Contains(rolled, "older than the one before") {
		t.Error("tunnel.log.1 kept the earlier rolled-over log instead of replacing it")
	}
	current := read(t, keeper.LogFile())
	if len(current) > 4096 {
		t.Errorf("tunnel.log was not started afresh: %d bytes", len(current))
	}
	if !strings.Contains(current, "starting it again in 1s") {
		t.Errorf("the keeper carried on writing somewhere else:\n%s", current)
	}
}

func TestTheKeeperStopsSSHWhenItIsAskedToStop(t *testing.T) {
	paths := machine(t)
	// exec rather than a child of the shell, so the process the keeper signals is
	// the one that is sleeping.
	fakeSSH(t, "exec sleep 30")
	keeper := New(paths, Options{Remote: saved(connected()), Sleep: func(context.Context, time.Duration) bool {
		t.Error("ssh exited by itself; this test is not checking what it thinks")
		return false
	}})

	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- keeper.Run(ctx) }()

	// Long enough for ssh to be running, short enough not to slow the suite.
	time.Sleep(200 * time.Millisecond)
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the keeper did not stop when it was asked to")
	}
	if written := read(t, keeper.LogFile()); !strings.Contains(written, "stopping: asked to") {
		t.Errorf("the log does not say it stopped on purpose:\n%s", written)
	}
}

func TestAnSSHThatIsNotInstalledIsReported(t *testing.T) {
	paths := machine(t)
	t.Setenv("PATH", t.TempDir())

	if _, err := SSH(); err == nil || !strings.Contains(err.Error(), "OpenSSH") {
		t.Errorf("SSH: %v, want an error naming what to install", err)
	}
	keeper := New(paths, Options{Remote: saved(connected())})
	err := keeper.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "not on the PATH") {
		t.Errorf("Run: %v, want an error saying ssh is missing", err)
	}
	// Said in the log too: the keeper runs with no terminal, so the log is the
	// only place somebody can read why it gave up.
	if written := read(t, keeper.LogFile()); !strings.Contains(written, "not on the PATH") {
		t.Errorf("the log does not say why it stopped:\n%s", written)
	}
}

func TestStopSignalsTheKeeperAndWaitsForIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in keeper is a shell command")
	}
	paths := machine(t)
	keeper := New(paths, Options{Remote: saved(connected())})

	stand := exec.Command("sleep", "30")
	if err := stand.Start(); err != nil {
		t.Fatal(err)
	}
	// Reaped on a goroutine. A signalled child that nobody waits for stays a
	// process as far as signal 0 is concerned, and a real `disconnect` is not the
	// keeper's parent, so without this the test would be checking something that
	// never happens.
	waited := make(chan struct{})
	go func() { _, _ = stand.Process.Wait(); close(waited) }()

	if err := os.MkdirAll(paths.Tunnel, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keeper.pidFile(), []byte(strconv.Itoa(stand.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, running := keeper.Running(); !running || pid != stand.Process.Pid {
		t.Fatalf("Running said %d, %v; want the stand-in's own id", pid, running)
	}

	if err := keeper.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop returned while the keeper was still running")
	}
}

func TestStopDoesNothingWhenNoKeeperIsRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in keeper is a shell command")
	}
	paths := machine(t)
	keeper := New(paths, Options{Remote: saved(connected())})

	// Nothing written at all: a reboot, or a second `disconnect`.
	if err := keeper.Stop(t.Context()); err != nil {
		t.Errorf("Stop with no keeper: %v", err)
	}

	// A process id left behind by a keeper that crashed. The process is gone, so
	// the file must not be believed: the number may belong to something else by
	// now.
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Tunnel, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keeper.pidFile(), []byte(strconv.Itoa(gone.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, running := keeper.Running(); running {
		t.Errorf("Running believed a stale process id: %d", pid)
	}
	if err := keeper.Stop(t.Context()); err != nil {
		t.Errorf("Stop with a stale process id: %v", err)
	}
}

func TestStartRunsTheKeeperInTheBackground(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in mnemo is a shell script")
	}
	paths := machine(t)
	bin := t.TempDir()
	recorded := filepath.Join(bin, "started")
	program := filepath.Join(bin, "mnemo")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > '" + recorded + "'\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	keeper := New(paths, Options{Remote: saved(connected()), Program: program})
	if err := keeper.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Started, not waited for: the point of Start is that it returns before the
	// keeper has done anything.
	for deadline := time.Now().Add(5 * time.Second); ; {
		if raw, err := os.ReadFile(recorded); err == nil {
			if strings.TrimSpace(string(raw)) != "tunnel" {
				t.Errorf("it started mnemo with %q, want tunnel", strings.TrimSpace(string(raw)))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the background keeper never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}
