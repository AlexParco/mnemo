package tunnel

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/lock"
)

// The loop's timings.
const (
	// settled is how long a run has to last before it counts as a connection that
	// worked. Anything shorter was a failure that took a while to happen — a
	// refused key, a host that is up but not listening — and it must not reset the
	// backoff, or a tunnel that dies after ten seconds would be retried forever
	// at one second apart.
	settled = 60 * time.Second
	// grabLock is how long Run gives the lock before it decides that it is the
	// second keeper. lock.Acquire tries once before it starts waiting, so a free
	// lock is taken straight away and this is only how long the loser takes to
	// find out. It is short because `connect` starts a keeper and then waits on
	// the forward.
	grabLock = 250 * time.Millisecond
	// stopSSH is how long ssh is given to die once the keeper is on its way out,
	// after which Wait gives up on it rather than hanging on a pipe.
	stopSSH = 5 * time.Second
)

// waits is the pause before each restart, and the last one repeats for as long
// as ssh keeps failing. It climbs because the usual reason for a failure in the
// first second is a server that is not there yet, and the usual reason for the
// hundredth is a machine that has been off all weekend.
var waits = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	30 * time.Second,
}

// Run holds the tunnel open until the context ends or the machine is no longer
// connected. It is the whole of `mnemo tunnel`.
//
// A second keeper returns nil without doing anything. That is not a special case
// to be avoided but the normal way the tunnel is started: every part that needs
// the server starts one, and all but the first find the lock taken and leave.
func (k *Keeper) Run(ctx context.Context) error {
	if err := k.placed(); err != nil {
		return err
	}

	taking, cancel := context.WithTimeout(ctx, grabLock)
	release, err := k.locker.Acquire(taking, k.paths.Tunnel)
	cancel()
	switch {
	case lock.IsTimeout(err):
		// Somebody else is keeping the tunnel, which is exactly what the caller
		// wanted. Exiting successfully is the answer.
		return nil
	case err != nil:
		return err
	}
	defer release()

	if err := os.WriteFile(k.pidFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing the tunnel keeper's process id to %s: %w", k.pidFile(), err)
	}
	// Removed on the way out so that `disconnect` does not signal a process id
	// that has since been handed to something else. A crash leaves it behind, and
	// Running checks that the process is really there for that reason.
	defer os.Remove(k.pidFile())

	logbook, err := openLogbook(k.LogFile())
	if err != nil {
		return err
	}
	defer logbook.Close()

	program, err := SSH()
	if err != nil {
		logbook.say(k.now(), "%s", err)
		return err
	}

	return k.loop(ctx, program, logbook)
}

// loop starts ssh, waits for it to die, and starts it again.
func (k *Keeper) loop(ctx context.Context, program string, logbook *logbook) error {
	failures := 0
	for {
		remote, saved, err := k.remote()
		if err != nil {
			logbook.say(k.now(), "cannot read the config: %s", err)
			return err
		}
		if !saved || !remote.Connected {
			// Said in the log because this is also how a keeper that lost the
			// termination signal ends, and somebody reading the log afterwards
			// needs to know it stopped on purpose.
			logbook.say(k.now(), "stopping: this machine is not connected to a server")
			return nil
		}

		logbook.say(k.now(), "forwarding 127.0.0.1:%d to %s through %s",
			remote.LocalPort, serverAddress(remote), remote.SSH)
		started := k.now()
		err = k.forward(ctx, program, remote, logbook)
		lasted := k.now().Sub(started)
		if ctx.Err() != nil {
			logbook.say(k.now(), "stopping: asked to")
			return nil
		}

		if lasted >= settled {
			// The connection worked for a while, so whatever just happened is a
			// new problem and deserves a fresh first retry rather than the wait
			// the last outage climbed to.
			failures = 0
		}
		pause := waits[min(failures, len(waits)-1)]
		failures++
		logbook.say(k.now(), "ssh exited after %s (%s); starting it again in %s",
			lasted.Round(time.Second), reason(err), pause)
		if !k.sleep(ctx, pause) {
			logbook.say(k.now(), "stopping: asked to")
			return nil
		}
	}
}

// forward is one run of ssh, from start to exit.
func (k *Keeper) forward(ctx context.Context, program string, remote config.Remote, out io.Writer) error {
	command := exec.CommandContext(ctx, program, forwardArgs(remote)...)
	// The log is handed over as a writer and not as the file it is, so that
	// os/exec copies ssh's output through a pipe and every line passes the
	// rotation check. Giving ssh the file descriptor itself would let it write
	// the log past a mebibyte behind the keeper's back.
	command.Stdout, command.Stderr = out, out
	// Nothing to type at: `-N` with BatchMode never asks anything, and a keeper
	// that could be made to wait on an answer would hold a dead tunnel open.
	command.Stdin = nil
	command.Cancel = func() error { return terminate(command.Process) }
	// The keeper has to be able to leave even if ssh ignores the signal, which it
	// can while it is in a system call of its own.
	command.WaitDelay = stopSSH
	return command.Run()
}

// forwardArgs is the command line from the specification, in its order.
//
// Each option earns its place: BatchMode so ssh never prompts, since there is no
// terminal to prompt at; ExitOnForwardFailure so a port that is already taken
// ends the run instead of leaving an ssh that is connected and forwarding
// nothing; and the two ServerAlive options so a link that went away silently —
// the usual fate of a laptop — is noticed in about 45 seconds rather than never.
func forwardArgs(remote config.Remote) []string {
	return []string{
		"-N",
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-L", fmt.Sprintf("127.0.0.1:%d:%s", remote.LocalPort, serverAddress(remote)),
		remote.SSH,
	}
}

// serverAddress is where the forward comes out: the server's own loopback, which
// is the only address it listens on.
func serverAddress(remote config.Remote) string {
	return "127.0.0.1:" + strconv.Itoa(remote.ServerPort)
}

// reason is how an exit reads in the log. ssh ending with no error at all is
// unusual enough to be worth saying plainly.
func reason(err error) string {
	if err == nil {
		return "no error"
	}
	return err.Error()
}
