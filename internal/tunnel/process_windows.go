package tunnel

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// detachedProcess is CREATE_NEW_CONSOLE's opposite: the child gets no console at
// all. The syscall package does not name it, so it is spelled out here rather
// than reached for in another module.
const detachedProcess = 0x00000008

// stillActive is what GetExitCodeProcess reports for a process that has not
// exited.
const stillActive = 259

// detach starts the keeper in its own process group and with no console.
//
// Windows has no sessions to leave, so these two flags do the same work: the
// group is what keeps a Ctrl-C meant for a chat from reaching the tunnel, and no
// console is what keeps a window from appearing and from holding the keeper to
// the terminal that started it.
func detach(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
}

// terminate stops a process. There is no signal to ask politely with on Windows,
// so this ends it; ssh is left to the operating system, which closes the socket
// when the process it belonged to goes.
func terminate(process *os.Process) error {
	if process == nil {
		return nil
	}
	err := process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// alive reports whether a process id is still a process on this machine.
//
// The handle alone is not the answer: a process that has exited can still be
// opened while something holds a handle to it, so its exit code is what decides.
// A process that cannot be opened at all is treated as gone.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}
