package tunnel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// pollStop is how often a stopping keeper is checked on. Short, because
// `disconnect` prints its line only once the tunnel is really down.
const pollStop = 50 * time.Millisecond

// Start runs `mnemo tunnel` in the background and does not wait for it.
//
// It always starts one, even when a keeper may already be running: deciding from
// here would mean trusting a process id file, and a keeper that finds the lock
// taken exits in a quarter of a second. One pointless process now and then is
// cheaper than a tunnel nobody started because a stale file said otherwise.
func (k *Keeper) Start() error {
	if err := k.placed(); err != nil {
		return err
	}
	program := k.program
	if program == "" {
		own, err := os.Executable()
		if err != nil {
			return fmt.Errorf("mnemo cannot find its own binary to start the tunnel keeper: %w", err)
		}
		program = own
	}

	command := exec.Command(program, "tunnel")
	// Left nil on purpose: os/exec gives a process with no streams the null
	// device, which is what the keeper wants. Inheriting this process's streams
	// would put ssh's complaints into whatever a tool is reading as a protocol,
	// and would keep the terminal that started it from ever closing.
	command.Stdin, command.Stdout, command.Stderr = nil, nil, nil
	detach(command)
	if err := command.Start(); err != nil {
		return fmt.Errorf("starting the tunnel keeper: %w", err)
	}
	// Waited on by a goroutine that nobody joins, only to reap it. The keeper
	// outlives most of its starters, and a starter that exits first leaves it to
	// the system; what this avoids is a long-lived `mnemo serve` collecting a
	// dead child for every time it brought the tunnel up.
	go func() { _ = command.Wait() }()
	return nil
}

// Running is the keeper's process id, and whether there is one at all.
//
// The process is checked, not only the file: the operating system frees the lock
// when a keeper dies, but a crash leaves the process id behind, and signalling a
// number that has since been handed to something else would stop the wrong
// program.
func (k *Keeper) Running() (int, bool) {
	raw, err := os.ReadFile(k.pidFile())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if !alive(pid) {
		return 0, false
	}
	return pid, true
}

// Stop asks the keeper to go away and waits for it, up to StopWait.
//
// It is what `disconnect` calls after it has written `connected = false`, in that
// order: a keeper that is signalled while the config still says connected would
// be started again by the next chat that wanted the server.
func (k *Keeper) Stop(ctx context.Context) error {
	pid, running := k.Running()
	if !running {
		// Nothing to stop is success. `disconnect` is a thing a person runs twice
		// without thinking, and after a reboot there is nothing there anyway.
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := terminate(process); err != nil {
		if _, stillRunning := k.Running(); !stillRunning {
			return nil
		}
		return fmt.Errorf("mnemo could not stop the tunnel keeper (process %d): %w", pid, err)
	}

	deadline := k.now().Add(StopWait)
	for k.now().Before(deadline) {
		if _, stillRunning := k.Running(); !stillRunning {
			return nil
		}
		if !k.sleep(ctx, pollStop) {
			return nil
		}
	}
	if _, stillRunning := k.Running(); !stillRunning {
		return nil
	}
	// Named with its process id, because that is what somebody needs to end it by
	// hand. The tunnel being up is harmless from here on: the config says this
	// machine is local, so nothing will use it.
	return fmt.Errorf("the tunnel keeper (process %d) did not stop within %s. mnemo is in local mode anyway; "+
		"end that process by hand if the tunnel is still up", pid, StopWait)
}
