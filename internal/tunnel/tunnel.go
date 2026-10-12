// Package tunnel keeps the SSH forward that a connected machine reaches its
// server through.
//
// A connected machine talks to its server over loopback: the server listens on
// 127.0.0.1 and never on a network, so the only way in is a local port forwarded
// by ssh. Something has to hold that forward open across a dropped link, a closed
// laptop lid and a reboot, and that something is the keeper: one background
// process per machine, restarting ssh for as long as the config says the machine
// is connected.
//
// Nothing here is on the path of a chat that is working. Every part that needs
// the server asks Reach first, which costs one health check when the forward is
// already up and starts the keeper when it is not. That is why a reboot needs no
// command from the user: opening a chat brings the connection back.
//
// This package does not decide whether the machine is connected. It reads the
// saved remote and obeys it, so `connect` and `disconnect` stay the only two
// places that choice is made.
package tunnel

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/lock"
)

// How long each step of the on-demand path is given, and how long a keeper is
// given to stop. They are exported because the commands that wait on the tunnel
// quote them to the user.
const (
	// HealthTimeout is the first check, the one every call pays for. It is short
	// on purpose: a chat asking for its memory must not wait on a dead tunnel.
	HealthTimeout = 2 * time.Second
	// RecheckEvery is how often the server is asked again while the keeper and
	// ssh are starting up.
	RecheckEvery = 250 * time.Millisecond
	// ReadyWait is how long the forward is given to come up before the caller is
	// told that it did not.
	ReadyWait = 10 * time.Second
	// StopWait is how long `disconnect` waits for the keeper to go away.
	StopWait = 5 * time.Second
)

// Options are the keeper's surroundings: where its answers come from, and the
// seams a test replaces. Every one of them has a working default, so
// `tunnel.New(paths, tunnel.Options{})` is the real thing.
type Options struct {
	// Remote reads the saved remote. It is asked again before every attempt
	// rather than once at the start, so a `disconnect` written while ssh was
	// running is obeyed on the next restart even if its signal was missed.
	Remote func() (config.Remote, bool, error)
	// Program is the mnemo binary that Start runs. Empty means this process's own
	// executable, which is what every caller but a test wants.
	Program string
	// Healthy asks the server whether it answers through the forward. Injected so
	// the on-demand path can be tested without a server, and so the relay can
	// pass its own check, which also compares the API version.
	Healthy func(ctx context.Context, remote config.Remote) error
	// Sleep waits, and reports whether the wait finished rather than the context
	// ending. Injected so a test can watch the backoff grow to thirty seconds
	// without taking a minute over it.
	Sleep func(ctx context.Context, pause time.Duration) bool
	// Now is the clock. The keeper measures how long a run lasted, and that is
	// what decides whether the backoff resets.
	Now func() time.Time
}

// A Keeper is one machine's tunnel: the loop that holds it open, and the checks
// and signals the commands around it use.
//
// One type serves all of them because they share the same three files — the
// lock, the process id and the log — and a second opinion about where those live
// would be a second tunnel.
type Keeper struct {
	paths  config.Paths
	locker *lock.Locker

	remote  func() (config.Remote, bool, error)
	program string
	healthy func(context.Context, config.Remote) error
	sleep   func(context.Context, time.Duration) bool
	now     func() time.Time
}

// New returns the keeper for this machine's state directory.
func New(paths config.Paths, opts Options) *Keeper {
	if opts.Remote == nil {
		opts.Remote = savedRemote
	}
	if opts.Healthy == nil {
		opts.Healthy = ask
	}
	if opts.Sleep == nil {
		opts.Sleep = wait
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Keeper{
		paths: paths,
		// The lock file lives in the tunnel directory, which is where the
		// specification puts it. lock.Locker names it after the directory it
		// guards, so the name is not literally keeper.lock; what the keeper needs
		// from it is that it is one file, in one place, held by the operating
		// system — which releases it when a crashed keeper dies.
		locker: lock.New(paths.Tunnel),

		remote:  opts.Remote,
		program: opts.Program,
		healthy: opts.Healthy,
		sleep:   opts.Sleep,
		now:     opts.Now,
	}
}

// LogFile is where the keeper's own lines and ssh's complaints go. `connect`
// shows its last lines when the tunnel does not come up, because ssh's refusal
// is the only thing that explains why.
func (k *Keeper) LogFile() string { return filepath.Join(k.paths.Logs, "tunnel.log") }

// pidFile holds the running keeper's process id, so `disconnect` can signal it.
func (k *Keeper) pidFile() string { return filepath.Join(k.paths.Tunnel, "keeper.pid") }

// placed refuses to work with paths that came back empty, which is what a
// machine with no home directory looks like. Writing a lock or a log relative to
// whatever directory the agent happened to be in would scatter them through the
// user's repositories.
func (k *Keeper) placed() error {
	if k.paths.Tunnel == "" || k.paths.Logs == "" {
		return fmt.Errorf("mnemo cannot work out where to keep the tunnel's lock and log: this machine has " +
			"no home directory. Set XDG_STATE_HOME to a directory mnemo may write in")
	}
	return nil
}

// SSH is the ssh program the keeper runs, or an error naming what is missing.
//
// `connect` calls it before it saves anything: a remote saved on a machine with
// no ssh client is a connection that can never come up, and the person would
// find that out from a chat that cannot reach its memory.
func SSH() (string, error) {
	program := "ssh"
	if runtime.GOOS == "windows" {
		// Named outright rather than left to LookPath's extension search, because
		// this is the client that ships with Windows and naming it is how the
		// error below tells somebody what to install.
		program = "ssh.exe"
	}
	path, err := exec.LookPath(program)
	if err != nil {
		return "", fmt.Errorf("%s is not on the PATH, so mnemo cannot open the tunnel. Install the OpenSSH "+
			"client and try again", program)
	}
	return path, nil
}

// savedRemote is the connection `connect` wrote, read fresh from the config.
//
// Fresh on every attempt, and not once at the start: `disconnect` writes
// `connected = false` and then signals, and a keeper that missed the signal has
// to find out the next time round. It goes through the resolved settings rather
// than through the file so that the keeper and every command agree about which
// entry counts and what `connected` means — one question, answered once.
func savedRemote() (config.Remote, bool, error) {
	file, err := config.Load()
	if err != nil {
		return config.Remote{}, false, err
	}
	connection := config.Resolve(file, config.Overrides{}).Connection
	if connection == nil {
		return config.Remote{}, false, nil
	}
	return config.Remote{
		Machine: connection.Machine, SSH: connection.SSH, ServerPort: connection.ServerPort,
		LocalPort: connection.LocalPort, Token: connection.Token, Connected: connection.Connected,
	}, true, nil
}

// wait is the real sleep: a timer the context can cut short.
func wait(ctx context.Context, pause time.Duration) bool {
	if pause <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
