package selfupdate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// oldSuffix marks the executable moved aside on Windows, where the running file
// cannot be replaced but can be renamed.
const oldSuffix = ".old"

// replace puts the new binary where the old one is, in one step.
//
// The new file is written next to the old one, never over it: a binary written
// in place is read as half of two different programs by anything that is already
// executing it, and a write that fails half way leaves a machine with no mnemo
// at all. Next to it also keeps the rename on one filesystem, which is what
// makes it a rename and not a copy.
func replace(executable string, binary []byte, goos string) error {
	directory := filepath.Dir(executable)
	mode := os.FileMode(0o755)
	if info, err := os.Stat(executable); err == nil {
		// The mode that was there is kept, because an install can be deliberately
		// narrower — a binary under /usr/local/bin owned by root, or one a person
		// made 0700. Only the owner's execute bit is forced on: a mnemo that
		// cannot be run is not an update, while forcing the group and other bits
		// would hand the binary to every account on the machine, which is the
		// opposite of what somebody who narrowed it asked for.
		mode = info.Mode().Perm() | 0o100
	}

	temporary, err := writeBeside(directory, filepath.Base(executable), binary, mode)
	if err != nil {
		return err
	}

	if goos == "windows" {
		// Windows will not let the running image be replaced, but it will let it
		// be renamed, and the handle the kernel holds follows the rename. The
		// leftover is deleted by the next mnemo command, not here: this process
		// is still running out of it.
		aside := executable + oldSuffix
		os.Remove(aside)
		if err := os.Rename(executable, aside); err != nil {
			os.Remove(temporary)
			return fmt.Errorf("moving %s aside: %w", executable, err)
		}
		if err := os.Rename(temporary, executable); err != nil {
			// Put the old one back before giving up. The alternative is a machine
			// with no mnemo on the PATH and a .old file nobody knows about.
			os.Rename(aside, executable)
			os.Remove(temporary)
			return fmt.Errorf("putting the new mnemo at %s: %w", executable, err)
		}
		return nil
	}

	if err := os.Rename(temporary, executable); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("replacing %s: %w", executable, err)
	}
	return nil
}

// writeBeside writes the new binary into the same directory under a name of its
// own, fully on disk before anybody renames it.
func writeBeside(directory, base string, binary []byte, mode os.FileMode) (string, error) {
	suffix := make([]byte, 6)
	rand.Read(suffix)
	path := filepath.Join(directory, "."+base+".mnemo-new-"+hex.EncodeToString(suffix))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return "", fmt.Errorf("mnemo cannot write next to itself in %s, so it cannot replace itself there: %w",
			directory, err)
	}
	if _, err := file.Write(binary); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("writing the new mnemo: %w", err)
	}
	// Flushed to the device before the rename. Without it a crash in the seconds
	// after an update can leave the name pointing at a file whose contents never
	// arrived, which is a machine that no longer has a working mnemo.
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("writing the new mnemo: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("writing the new mnemo: %w", err)
	}
	// The mode is set explicitly as well as passed to OpenFile, because the
	// process umask takes bits off the mode a create asks for, and an update is
	// not the moment to discover the new binary is not executable.
	if err := os.Chmod(path, mode); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("making the new mnemo executable: %w", err)
	}
	return path, nil
}

// RemoveOldExecutable deletes the executable a Windows update moved aside.
//
// It exists because the update itself cannot do it: the process doing the
// updating is running out of that very file. So every mnemo command calls this
// on the way in, and the first one after an update is the one that clears it.
// A failure is not reported — the file is harmless, and a command that printed
// an error about the last update's leftovers would be noise in front of whatever
// the person actually asked for.
func RemoveOldExecutable(executable string) {
	if executable == "" {
		executable, _ = os.Executable()
	}
	if executable == "" {
		return
	}
	os.Remove(executable + oldSuffix)
}
