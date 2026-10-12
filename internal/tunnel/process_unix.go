//go:build !windows

package tunnel

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// detach puts the keeper in a session of its own.
//
// Without this it would belong to the terminal that started it, which means the
// Ctrl-C that stops a chat would stop the tunnel too, and closing the window
// would take it down with the shell. A new session also detaches it from the
// controlling terminal, so it cannot be put in the foreground by accident.
func detach(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// terminate asks a process to stop. SIGTERM rather than SIGKILL: the keeper has
// an ssh to shut down before it goes, and ssh has a connection to close.
func terminate(process *os.Process) error {
	if process == nil {
		return nil
	}
	err := process.Signal(syscall.SIGTERM)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// alive reports whether a process id is still a process on this machine.
//
// Signal 0 is the portable way to ask: it runs the existence and permission
// checks and delivers nothing. A process the user cannot signal is still a
// process, so being refused permission counts as alive — the alternative would
// be a second keeper deciding the first one had died.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM)
}
