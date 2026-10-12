// Package service installs, starts, restarts and removes the mnemo server as a
// service on the machine that hosts it.
//
// The service files are written directly rather than through a service library,
// so what lands on disk is exactly what docs/specs/remote.md says and nothing a
// dependency decided. Everything that leaves this package goes through one
// injected command runner, so a test never touches systemd, launchd or sudo.
//
// Nothing here knows the token. It is in the config file, mode 0600, and the
// service reads it from there: a token in a unit file or in a plist would be
// world-readable, and a token in the command line would be visible to every
// process on the machine.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/AlexParco/mnemo/internal/config"
)

// Label is the name macOS knows the service by, in launchd's reverse-DNS form.
// It is also the plist's file name.
const Label = "io.github.alexparco.mnemo"

// UnitName is the name systemd knows the service by.
const UnitName = "mnemo.service"

// Outcome is what happened to the service.
type Outcome string

const (
	Installed Outcome = "installed"
	Restarted Outcome = "restarted"
	Removed   Outcome = "removed"
	Nothing   Outcome = "nothing to remove"
	Running   Outcome = "running"
	Refused   Outcome = "refused"
)

// Result is one line of output: what happened, where, and what the person still
// has to do by hand. A service is installed with tools that fail for reasons
// mnemo cannot fix — no session bus, no sudo, lingering off — and a bare error
// would leave the person with no next step, so every refusal carries one.
type Result struct {
	Outcome Outcome
	// Detail says where the service file is, or why it was refused.
	Detail string
	// Advice is what the user has to do by hand, when anything is.
	Advice string
}

func (r Result) String() string {
	line := string(r.Outcome)
	if r.Detail != "" {
		line += ": " + r.Detail
	}
	if r.Advice != "" {
		line += "\n  " + strings.ReplaceAll(r.Advice, "\n", "\n  ")
	}
	return line
}

// Platform is the kind of service machinery a machine has. The three differ in
// everything: the file, its place, the tool, and whether sudo is involved.
type Platform string

const (
	Linux   Platform = "linux"
	MacOS   Platform = "darwin"
	Windows Platform = "windows"
)

// CurrentPlatform is the platform of the machine this binary runs on.
//
// It is read once, into a field of Manager, instead of being consulted wherever
// a decision depends on it. A test machine is only ever one platform, and the
// rendered unit file and the plist have to be checked on whichever machine runs
// the suite — so the platform is data, not something the code looks up.
func CurrentPlatform() Platform {
	switch runtime.GOOS {
	case "linux":
		return Linux
	case "darwin":
		return MacOS
	case "windows":
		return Windows
	default:
		// Every other Unix has its own init, and mnemo has never been tried on
		// one. Treated as Linux: systemd is there often enough that trying and
		// reporting the tool's own error is more use than refusing by name.
		return Linux
	}
}

// Manager is this machine's mnemo service: everything the service files and the
// commands need, with the two things a test has to replace — the platform and
// the command runner — as fields rather than as calls into the operating system.
type Manager struct {
	Platform Platform
	// User owns the service: it is the systemd user whose lingering is checked,
	// and the plist's UserName. The server reads the user's store, so the service
	// must not run as anybody else.
	User string
	// Home is that user's home directory, which the macOS daemon has to be told
	// because a LaunchDaemon starts with no environment at all.
	Home string
	// ConfigHome is where the systemd user unit goes: $XDG_CONFIG_HOME, else
	// ~/.config.
	ConfigHome string
	// Logs is the state directory's logs directory, where the macOS daemon's
	// output goes. systemd keeps its own journal, so Linux does not use it.
	Logs string
	// LaunchDaemons is the directory the macOS plist is installed into. It is a
	// field, and not the constant it almost always holds, only so that a test can
	// point it at a directory it is allowed to write in: nothing can install into
	// the real one without being root.
	LaunchDaemons string
	// Binary is the absolute path of the mnemo the service runs. A service has no
	// PATH worth trusting, and a name would resolve to whatever a future PATH
	// held.
	Binary string
	// Run runs one command and gives back its combined output. It carries the
	// program's name as well as its arguments, because this package drives four
	// of them — systemctl, loginctl, launchctl and sudo — and a test asserts the
	// order they were run in.
	Run func(name string, args ...string) (string, error)
}

// New describes the service of the machine these settings belong to. binary is
// the absolute path of the mnemo the service will run.
func New(settings config.Settings, binary string) *Manager {
	return &Manager{
		Platform:      CurrentPlatform(),
		User:          currentUser(),
		Home:          home(),
		ConfigHome:    configHome(),
		Logs:          settings.Paths.Logs,
		LaunchDaemons: LaunchDaemons,
		Binary:        binary,
		Run:           command,
	}
}

// Install writes the service file and starts the service, as the platform wants
// it done.
func (m *Manager) Install() (Result, error) {
	switch m.Platform {
	case Windows:
		return refuseWindows(), nil
	case MacOS:
		return m.installMacOS()
	default:
		return m.installLinux()
	}
}

// Restart restarts the service, leaving the config alone. The token lives in the
// config, so a restart picks up a rotated one without this package knowing it.
func (m *Manager) Restart() (Result, error) {
	switch m.Platform {
	case Windows:
		return refuseWindows(), nil
	case MacOS:
		return m.restartMacOS()
	default:
		return m.restartLinux()
	}
}

// Remove stops the service and deletes its file. The store, the mailbox and the
// config are not its business.
func (m *Manager) Remove() (Result, error) {
	switch m.Platform {
	case Windows:
		return refuseWindows(), nil
	case MacOS:
		return m.removeMacOS()
	default:
		return m.removeLinux()
	}
}

// State is what mnemo can tell about the service without asking for a password.
type State struct {
	// Installed is whether the service file is there.
	Installed bool
	// Path is that file, whether or not it exists, so a report can name it.
	Path    string
	Running bool
	// RunningKnown is false when mnemo could not find out. On macOS it always is:
	// launchd only answers root, and prompting for a password to print a status
	// line is worse than saying nothing. The caller has a better answer anyway —
	// WaitHealthy asks the server itself.
	RunningKnown bool
}

// State reports whether the service is installed and, where that can be found
// out without sudo, whether it is running.
func (m *Manager) State() State {
	switch m.Platform {
	case Windows:
		return State{}
	case MacOS:
		return State{Installed: exists(m.PlistPath()), Path: m.PlistPath()}
	default:
		state := State{Installed: exists(m.UnitPath()), Path: m.UnitPath()}
		if out, err := m.Run("systemctl", "--user", "is-active", UnitName); hasBus(out, err) {
			state.Running, state.RunningKnown = strings.TrimSpace(out) == "active", true
		}
		return state
	}
}

// refuseWindows is the one answer Windows gets from this package. Running the
// server as a Windows service is out of scope, and the server itself is not: the
// refusal says what still works, because a person on Windows can host perfectly
// well by leaving a terminal open.
func refuseWindows() Result {
	return Result{
		Outcome: Refused,
		Detail:  "mnemo cannot run the server as a service on Windows",
		Advice: "Nothing was installed or changed. You can still host from this machine by running the server " +
			"by hand, in a terminal you leave open:\n  mnemo serve --http",
	}
}

// command is the real runner: combined output, because these tools say what went
// wrong on whichever stream they feel like.
func command(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// alreadyThere reports whether a command failed only because what it would
// create is already there, which is what a second setup on the same machine
// looks like.
func alreadyThere(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "already") || strings.Contains(lower, "exists")
}

// toolOutput puts a tool's own words at the top of the advice, because the
// reason a service failed to load is in them and nowhere else.
func toolOutput(out string) string {
	if trimmed := strings.TrimSpace(out); trimmed != "" {
		return trimmed + "\n"
	}
	return ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeFile replaces a file in one step, so no tool ever reads half of one.
func writeFile(path string, content string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	temp := path + ".mnemo-tmp"
	if err := os.WriteFile(temp, []byte(content), mode); err != nil {
		return fmt.Errorf("writing %s: %w", temp, err)
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

func currentUser() string {
	if who, err := user.Current(); err == nil && who.Username != "" {
		return who.Username
	}
	// os/user needs cgo or a readable passwd file, and a container may have
	// neither. The variables are what the login left behind.
	for _, name := range []string{"USER", "LOGNAME"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func home() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return dir
}

// configHome is $XDG_CONFIG_HOME, else ~/.config. A relative value is ignored,
// as the specification it comes from says, so a stray `XDG_CONFIG_HOME=config`
// cannot put the unit file under the working directory of whoever ran setup.
func configHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); filepath.IsAbs(dir) {
		return dir
	}
	if home() == "" {
		return ""
	}
	return filepath.Join(home(), ".config")
}
