package mailbox

import (
	"os"
	"syscall"
)

// processAlive reports whether a process is still running on this machine.
//
// Signal 0 is the portable way to ask: it performs the permission checks and the
// existence check and delivers nothing. A process the user cannot signal is still
// a process, so EPERM counts as alive — the alternative would be handing a held
// name to a second chat because the first one belongs to another account.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return err == os.ErrPermission || err == syscall.EPERM
}
