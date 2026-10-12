package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// macOS gets a LaunchDaemon, not a LaunchAgent. An agent only runs while the
// user is logged in, and it dies with the login session — so the one thing a
// server is for, being there when nobody is sitting at the machine, is the one
// thing an agent cannot do. A daemon lives in /Library/LaunchDaemons, which only
// root can write, which is why this is the only platform where mnemo uses sudo.
//
// A daemon starts with no environment at all, so the plist has to say everything
// the server needs: who it runs as, where HOME is, and a PATH with git on it.

// ServicePath is the PATH the macOS daemon runs with. A daemon inherits nothing,
// and mnemo shells out to git, so without this the server could not push to the
// hub: Homebrew's git is in neither /usr/bin nor /bin.
const ServicePath = "/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin"

// LaunchDaemons is where macOS keeps the plists it starts at boot.
const LaunchDaemons = "/Library/LaunchDaemons"

// PlistPath is where the LaunchDaemon's plist goes.
func (m *Manager) PlistPath() string {
	directory := m.LaunchDaemons
	if directory == "" {
		directory = LaunchDaemons
	}
	return filepath.Join(directory, Label+".plist")
}

// LogFile is where the daemon's output goes. systemd has a journal; launchd
// writes to files, and the service's files belong under mnemo's state directory
// with everything else that is this machine's and not memory.
func (m *Manager) LogFile() string {
	if m.Logs == "" {
		return ""
	}
	return filepath.Join(m.Logs, "server.log")
}

// Environment is the environment the service runs with. It is exported because
// anything that wants to check what the service will be able to do — git's SSH
// authentication, above all — has to ask under these variables and not under the
// shell's.
//
// It describes the macOS daemon. A systemd user service inherits the user
// instance's environment instead, which mnemo does not set and cannot describe.
func (m *Manager) Environment() []string {
	return []string{"HOME=" + m.Home, "PATH=" + ServicePath}
}

// Plist is the LaunchDaemon's contents, with every key docs/specs/remote.md
// lists and none it does not.
func (m *Manager) Plist() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" ` +
		`"http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	key := func(name string) { fmt.Fprintf(&b, "\t<key>%s</key>\n", name) }
	str := func(indent, value string) { fmt.Fprintf(&b, "%s<string>%s</string>\n", indent, escape(value)) }

	key("Label")
	str("\t", Label)
	key("UserName")
	str("\t", m.User)
	key("ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, argument := range []string{m.Binary, "serve", "--http"} {
		str("\t\t", argument)
	}
	b.WriteString("\t</array>\n")
	key("EnvironmentVariables")
	b.WriteString("\t<dict>\n")
	b.WriteString("\t\t<key>HOME</key>\n")
	str("\t\t", m.Home)
	b.WriteString("\t\t<key>PATH</key>\n")
	str("\t\t", ServicePath)
	b.WriteString("\t</dict>\n")
	key("RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("KeepAlive")
	b.WriteString("\t<true/>\n")
	key("StandardOutPath")
	str("\t", m.LogFile())
	key("StandardErrorPath")
	str("\t", m.LogFile())
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// escape makes a value safe inside an XML string. A home directory or a binary
// path can contain `&`, and launchd refuses a plist it cannot parse — the server
// would simply never start, with the reason buried in a system log.
func escape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func (m *Manager) installMacOS() (Result, error) {
	result := Result{Detail: m.PlistPath()}
	// launchd opens StandardOutPath itself and will not create the directory for
	// it. On a machine whose state directory is still fresh, the daemon would
	// fail to start with nothing written anywhere to say why.
	if m.LogFile() != "" {
		if err := os.MkdirAll(m.Logs, 0o700); err != nil {
			return result, fmt.Errorf("creating %s: %w", m.Logs, err)
		}
	}

	// Written to a temporary file first and copied in by `install`, because the
	// destination is root's and this process is not root. `install` sets the
	// owner, the group and the mode in the same step that puts the file there, so
	// there is never a moment where /Library/LaunchDaemons holds a file the user
	// could still edit.
	temp, err := os.CreateTemp("", "mnemo-*.plist")
	if err != nil {
		return result, fmt.Errorf("writing the plist to a temporary file: %w", err)
	}
	defer os.Remove(temp.Name())
	if _, err := temp.WriteString(m.Plist()); err != nil {
		temp.Close()
		return result, fmt.Errorf("writing %s: %w", temp.Name(), err)
	}
	if err := temp.Close(); err != nil {
		return result, fmt.Errorf("writing %s: %w", temp.Name(), err)
	}

	if out, err := m.Run("sudo", "install", "-m", "644", "-o", "root", "-g", "wheel",
		temp.Name(), m.PlistPath()); err != nil {
		result.Outcome, result.Detail = Refused, "installing the plist needs sudo, and it failed"
		result.Advice = toolOutput(out) + "Nothing was installed. sudo asks for your password in this terminal, so " +
			"run `mnemo server setup` where you can type it, and from an account that may use sudo."
		return result, nil
	}
	out, err := m.Run("sudo", "launchctl", "bootstrap", "system", m.PlistPath())
	if err != nil && !alreadyThere(out) {
		result.Outcome, result.Detail = Refused, "launchctl would not load the service"
		result.Advice = toolOutput(out) + "The plist is at " + m.PlistPath() +
			", so the service exists but is not running.\nRun: sudo launchctl bootstrap system " + m.PlistPath()
		return result, nil
	}
	result.Outcome = Installed
	if err != nil {
		// Already loaded: this is a second setup on the same machine. launchd is
		// still running the plist it loaded the first time, so the one that was
		// just written does not take effect until the service is kicked.
		result.Advice = "The service was already loaded, so launchd is still running the previous plist.\n" +
			"Run: mnemo server restart"
	}
	return result, nil
}

func (m *Manager) restartMacOS() (Result, error) {
	result := Result{Detail: Label}
	// kickstart -k stops it and starts it again under launchd, rather than
	// bootout-and-bootstrap: the service stays loaded, so a failure leaves a
	// running server rather than none at all.
	out, err := m.Run("sudo", "launchctl", "kickstart", "-k", "system/"+Label)
	if err != nil {
		result.Outcome, result.Detail = Refused, "launchctl would not restart the service"
		result.Advice = toolOutput(out) + "Run: sudo launchctl kickstart -k system/" + Label +
			"\nAnd read why it stopped in " + m.LogFile()
		return result, nil
	}
	result.Outcome = Restarted
	return result, nil
}

func (m *Manager) removeMacOS() (Result, error) {
	result := Result{Detail: m.PlistPath()}
	if !exists(m.PlistPath()) {
		result.Outcome = Nothing
		return result, nil
	}
	out, err := m.Run("sudo", "launchctl", "bootout", "system/"+Label)
	if err != nil && !notLoaded(out) {
		result.Outcome, result.Detail = Refused, "launchctl would not unload the service"
		result.Advice = toolOutput(out) + "The plist was left at " + m.PlistPath() +
			".\nRun: sudo launchctl bootout system/" + Label
		return result, nil
	}
	// Deleted through sudo as well: the file is root's, so this process cannot
	// unlink it, and leaving it there would start the server again at the next
	// boot after the person was told it had been removed.
	if out, err := m.Run("sudo", "rm", "-f", m.PlistPath()); err != nil {
		result.Outcome, result.Detail = Refused, "the service is stopped, but its plist is still there"
		result.Advice = toolOutput(out) + "It will start again at the next boot until the file is gone.\n" +
			"Run: sudo rm -f " + m.PlistPath()
		return result, nil
	}
	result.Outcome = Removed
	return result, nil
}

// notLoaded reports whether bootout failed only because the service was not
// loaded, which is what removing a service that was installed but never started
// looks like.
func notLoaded(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "no such process") || strings.Contains(lower, "not loaded") ||
		strings.Contains(lower, "could not find specified service")
}
