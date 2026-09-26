// Package config resolves mnemo's settings and the paths it keeps things in.
//
// Two things live here. Paths says where the store, the state directory, the
// locks and the mailbox are on this machine. Settings is every tunable, resolved
// from the environment, the config file and the defaults, each one remembering
// where its value came from so `mnemo config` can show it.
//
// Nothing here touches the store. A wrong path is worse than a missing one, so
// resolution never creates a directory as a side effect of being asked a
// question: creating is the caller's decision, at the moment it writes.
package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Paths is where mnemo keeps things on this machine.
type Paths struct {
	// Config is the config file itself, which need not exist.
	Config string
	// Store is the memory store: a git repository of its own.
	Store string
	// State holds everything that belongs to this machine and is not memory.
	State    string
	Locks    string
	Mailbox  string
	Tunnel   string
	Logs     string
	Reminder string
}

// env reads a variable, treating a blank value as unset. A variable exported
// empty by a wrapper script is the same as one that was never set: it means the
// person did not choose, not that they chose nothing. The value itself comes
// back as it was written, because trimming a path would be this package
// changing a setting behind the user's back.
func env(name string) string {
	value := os.Getenv(name)
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return value
}

// home is the user's home directory. On Windows it is %USERPROFILE%.
func home() string {
	if dir, err := os.UserHomeDir(); err == nil && dir != "" {
		return dir
	}
	// Without a home there is nowhere to default to, and guessing / would put a
	// store somewhere the user would never look. The empty string travels up and
	// whoever needs the path reports it.
	return ""
}

// under joins a base and the rest, and answers empty when the base is empty, so
// a missing home does not become a path relative to the working directory.
func under(base string, rest ...string) string {
	if base == "" {
		return ""
	}
	return filepath.Join(append([]string{base}, rest...)...)
}

// xdg reads an XDG base directory, which counts only when it is absolute: the
// specification says a relative value must be ignored.
func xdg(name, fallback string) string {
	if dir := env(name); filepath.IsAbs(dir) {
		return dir
	}
	return fallback
}

// expandHome turns a leading `~` into the home directory. A person typing
// `~/memories` into a config file means their home, and without this the value
// is merely not absolute, so it would become a directory literally called `~`
// next to wherever the shell happened to be. `~user` is left alone: resolving
// another account's home is not something mnemo should guess at.
func expandHome(path string) (string, bool) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, true
	}
	dir := home()
	if dir == "" {
		return "", false
	}
	return filepath.Join(dir, strings.TrimPrefix(path[1:], string(filepath.Separator))), true
}

// absolute resolves a relative path against the working directory, once. From
// then on the value is absolute, so a later change of directory — a server that
// serves several chats, a command that walks into a repository — cannot move the
// store out from under the process.
func absolute(path string) string {
	if path == "" {
		return ""
	}
	path, ok := expandHome(path)
	if !ok {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	cwd, err := os.Getwd()
	if err != nil {
		// Returning the relative path would break the promise above: it would move
		// with the process. Empty travels up and is reported, like a missing home.
		return ""
	}
	return filepath.Join(cwd, path)
}

// ConfigFile is where the config file is, with no file needed to find out.
func ConfigFile() string {
	if path := env("MNEMO_CONFIG"); path != "" {
		return absolute(path)
	}
	return under(xdg("XDG_CONFIG_HOME", under(home(), ".config")), "mnemo", "config.toml")
}

// StateDir is where this machine's own state lives: locks, the mailbox, logs.
// It is never the store, because none of it is memory and none of it syncs.
func StateDir() string {
	return under(xdg("XDG_STATE_HOME", under(home(), ".local", "state")), "mnemo")
}

// defaultStore is the store's path when nothing points anywhere else.
func defaultStore() string {
	return under(xdg("XDG_DATA_HOME", under(home(), ".local", "share")), "mnemo")
}

// Where resolves every path, given the file's settings.
func (f *File) Where() Paths {
	state := StateDir()
	paths := Paths{
		Config:   ConfigFile(),
		Store:    defaultStore(),
		State:    state,
		Locks:    under(state, "locks"),
		Mailbox:  under(state, "mailbox"),
		Tunnel:   under(state, "tunnel"),
		Logs:     under(state, "logs"),
		Reminder: under(state, "suggest-save"),
	}
	if dir := env("MNEMO_DIR"); dir != "" {
		paths.Store = absolute(dir)
	} else if f != nil && f.Store.Dir != "" {
		paths.Store = absolute(f.Store.Dir)
	}
	if dir := env("MNEMO_MAILBOX_DIR"); dir != "" {
		paths.Mailbox = absolute(dir)
	} else if f != nil && f.Mailbox.Dir != "" {
		paths.Mailbox = absolute(f.Mailbox.Dir)
	}
	return paths
}
