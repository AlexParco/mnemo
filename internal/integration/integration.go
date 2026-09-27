// Package integration registers mnemo in the tools that run it: Claude Code,
// Codex and opencode.
//
// All three come down to editing a file somebody else owns. That shapes
// everything here: the edit is marked so it can be found again, a copy of the
// file is kept before the first change, the write is atomic, and an entry mnemo
// did not write is never touched — it is reported, and the user decides.
package integration

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Outcome is what happened to one tool.
type Outcome string

const (
	Added   Outcome = "added"
	Current Outcome = "already up to date"
	Removed Outcome = "removed"
	Nothing Outcome = "nothing to remove"
	Skipped Outcome = "skipped (not found)"
	Refused Outcome = "refused"
)

// Result is one line of output.
type Result struct {
	Tool    string
	Outcome Outcome
	// Detail says where the file is, or why it was refused.
	Detail string
	// Advice is what the user has to do by hand, when anything is.
	Advice string
}

func (r Result) String() string {
	line := fmt.Sprintf("%-10s %s", r.Tool, r.Outcome)
	if r.Detail != "" {
		line += ": " + r.Detail
	}
	if r.Advice != "" {
		line += "\n  " + strings.ReplaceAll(r.Advice, "\n", "\n  ")
	}
	return line
}

// Names are the tools, in the order they are reported.
var Names = []string{"claude", "codex", "opencode"}

// Executables are what has to be on the PATH for a tool to count as installed.
var executables = map[string]string{"claude": "claude", "codex": "codex", "opencode": "opencode"}

// Executable is what has to be on the PATH for a tool to count as installed.
func Executable(tool string) string { return executables[tool] }

// Installed reports whether a tool is on this machine.
func Installed(tool string) bool {
	_, err := exec.LookPath(executables[tool])
	return err == nil
}

// MnemoPath is the mnemo the entries point at.
//
// Codex and opencode get an absolute path, because a tool started from a desktop
// launcher may not have ~/.local/bin on its PATH. Symbolic links are left
// unresolved on purpose: a package manager's versioned directory would break the
// path at the next upgrade.
func MnemoPath() (string, error) {
	running, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding this executable: %w", err)
	}
	onPath, err := exec.LookPath("mnemo")
	if err != nil {
		return running, nil
	}
	// Prefer the name on the PATH when it is this same file, so an entry keeps
	// working after an update that replaces the binary in place.
	if same, err := sameFile(onPath, running); err == nil && same {
		return onPath, nil
	}
	return running, nil
}

func sameFile(a, b string) (bool, error) {
	left, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	right, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(left, right), nil
}

// OnPath reports whether `mnemo` can be found by name, which is what the Claude
// Code plugin relies on: a plugin cannot know an absolute path.
func OnPath() bool {
	_, err := exec.LookPath("mnemo")
	return err == nil
}

// backupSuffix marks the copy kept before mnemo first changes someone's file.
const backupSuffix = ".mnemo-backup"

// backup keeps a copy of the file as it is now, before mnemo changes it.
//
// It used to keep the first snapshot for ever and never refresh it, which is the
// wrong way round: the only thing a backup protects against is the edit mnemo is
// about to make, and by the time somebody reaches for it, a snapshot from the
// first run has lost every change they made since.
func backup(path string) error {
	target := path + backupSuffix
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return write(target, content)
}

// write replaces a file in one step, so a tool reading it never sees half.
//
// The mode of the file that was there is kept: Codex's config can carry provider
// keys, and turning a 0600 file into a 0644 one would publish them to every
// account on the machine. A symlink is followed rather than replaced, because a
// config fed from a dotfiles repository must keep being fed from it.
func write(path string, content []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	suffix := make([]byte, 6)
	rand.Read(suffix)
	temp := filepath.Join(dir, "."+filepath.Base(path)+".mnemo-tmp-"+hex.EncodeToString(suffix))
	if err := os.WriteFile(temp, content, mode); err != nil {
		return fmt.Errorf("writing %s: %w", temp, err)
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

func read(path string) (string, bool, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(content), true, nil
}

// home is the user's home directory, for the default locations.
func home() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return dir
}
