package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Linux gets a systemd user service, and mnemo never runs sudo for it. A system
// unit would need root to install and would run the server as root, which is the
// wrong user: the store, the mailbox and the config belong to the person, and a
// root service writing into them would leave files they cannot change. The price
// is lingering, which is the one thing here that does need sudo — so it is
// checked and printed, never run.

// UnitPath is where the systemd user unit goes.
func (m *Manager) UnitPath() string {
	if m.ConfigHome == "" {
		return ""
	}
	return filepath.Join(m.ConfigHome, "systemd", "user", UnitName)
}

// Unit is the unit file's contents, exactly as docs/specs/remote.md gives them.
//
// ExecStart is an absolute path: a unit's PATH is systemd's, not the shell's, so
// `mnemo` by name would not be found — or worse, would be found later and be a
// different binary than the one that installed the service.
func (m *Manager) Unit() string {
	return strings.Join([]string{
		"[Unit]",
		"Description=mnemo server",
		"After=network.target",
		"",
		"[Service]",
		"ExecStart=" + m.Binary + " serve --http",
		"Restart=on-failure",
		"RestartSec=2",
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	}, "\n")
}

func (m *Manager) installLinux() (Result, error) {
	result := Result{Detail: m.UnitPath()}
	if m.UnitPath() == "" {
		result.Outcome, result.Detail = Refused, "mnemo cannot work out where this user's config directory is"
		result.Advice = "Nothing was installed. Set HOME, or XDG_CONFIG_HOME to an absolute path, and run this again."
		return result, nil
	}
	// Asked before anything is written. A unit file installed where no systemd
	// user instance will ever read it is worse than a refusal: setup would say it
	// started a server that is not running.
	if out, err := m.Run("systemctl", "--user", "show-environment"); !hasBus(out, err) {
		result.Outcome, result.Detail = Refused, "systemctl --user has no systemd to talk to"
		result.Advice = toolOutput(out) + "This user has no systemd session bus, so mnemo cannot install a user " +
			"service: that is what a container without systemd looks like, or a connection that gets no login " +
			"session.\nNothing was installed.\nOn a machine with systemd, log in so the user instance starts " +
			"(`ssh` with a login session, or `machinectl shell`) and run `mnemo server setup` again. Otherwise run " +
			"the server by hand, in a terminal you leave open:\n  mnemo serve --http"
		return result, nil
	}

	// Mode 0644: the unit is read by the user's own systemd and carries no
	// secret. The token stays in the config file, at 0600.
	if err := writeFile(m.UnitPath(), m.Unit(), 0o644); err != nil {
		return result, err
	}
	if out, err := m.Run("systemctl", "--user", "daemon-reload"); err != nil {
		result.Outcome, result.Detail = Refused, "systemctl --user daemon-reload failed"
		result.Advice = toolOutput(out) + "The unit file was written to " + m.UnitPath() +
			" but systemd has not read it.\nRun: systemctl --user daemon-reload && systemctl --user enable --now " +
			UnitName
		return result, nil
	}
	if out, err := m.Run("systemctl", "--user", "enable", "--now", UnitName); err != nil {
		result.Outcome, result.Detail = Refused, "systemctl --user enable --now "+UnitName+" failed"
		result.Advice = toolOutput(out) + "The unit file is at " + m.UnitPath() +
			", so the service exists but is not running.\nRun: systemctl --user enable --now " + UnitName +
			"\nAnd read why it stopped with: journalctl --user -u " + UnitName + " -n 50"
		return result, nil
	}

	result.Outcome = Installed
	// Checked after the service is up, because the answer does not change
	// whether mnemo installs: a server that stops at logout is still a server
	// until then, and the person decides whether to fix that.
	if !m.lingers() {
		result.Advice = "warning: lingering is off for this user, so the service stops when you log out and does " +
			"not come back when the machine reboots. mnemo does not run sudo on Linux, so turn it on yourself:\n  " +
			m.lingerCommand()
	}
	return result, nil
}

func (m *Manager) restartLinux() (Result, error) {
	result := Result{Detail: UnitName}
	out, err := m.Run("systemctl", "--user", "restart", UnitName)
	if err != nil {
		result.Outcome, result.Detail = Refused, "systemctl --user restart "+UnitName+" failed"
		result.Advice = toolOutput(out) + "Run: systemctl --user restart " + UnitName +
			"\nAnd read why it stopped with: journalctl --user -u " + UnitName + " -n 50"
		return result, nil
	}
	result.Outcome = Restarted
	return result, nil
}

func (m *Manager) removeLinux() (Result, error) {
	result := Result{Detail: m.UnitPath()}
	if m.UnitPath() == "" || !exists(m.UnitPath()) {
		result.Outcome = Nothing
		return result, nil
	}
	// Stopped before the file goes: with the unit deleted first, systemd no
	// longer knows the name and the running server would stay up with nothing
	// able to stop it but a kill.
	if out, err := m.Run("systemctl", "--user", "disable", "--now", UnitName); err != nil {
		result.Outcome, result.Detail = Refused, "systemctl --user disable --now "+UnitName+" failed"
		result.Advice = toolOutput(out) + "The unit file was left at " + m.UnitPath() +
			".\nRun: systemctl --user disable --now " + UnitName
		return result, nil
	}
	if err := os.Remove(m.UnitPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if out, err := m.Run("systemctl", "--user", "daemon-reload"); err != nil {
		result.Outcome, result.Detail = Refused, "systemctl --user daemon-reload failed"
		result.Advice = toolOutput(out) + "The service is stopped and the unit file is gone; systemd still has the " +
			"old unit loaded.\nRun: systemctl --user daemon-reload"
		return result, nil
	}
	result.Outcome = Removed
	return result, nil
}

// lingers reports whether this user's services keep running after they log out.
func (m *Manager) lingers() bool {
	if m.User == "" {
		return false
	}
	out, err := m.Run("loginctl", "show-user", m.User, "--property=Linger")
	if err != nil {
		// Not knowing is treated as off, and the warning is printed. The opposite
		// default would promise a server that survives logout on the strength of
		// a command that did not answer.
		return false
	}
	return strings.TrimSpace(out) == "Linger=yes"
}

// lingerCommand is the line the person has to type, with their own user in it.
func (m *Manager) lingerCommand() string {
	who := m.User
	if who == "" {
		who = "$USER"
	}
	return "sudo loginctl enable-linger " + who
}

// hasBus reports whether `systemctl --user` reached systemd at all. It is not
// the same question as whether the command succeeded: `is-active` fails when the
// service is simply stopped, which says nothing about the bus.
func hasBus(out string, err error) bool {
	if err == nil {
		return true
	}
	lower := strings.ToLower(out)
	for _, phrase := range []string{"failed to connect to bus", "no medium found", "dbus_session_bus_address"} {
		if strings.Contains(lower, phrase) {
			return false
		}
	}
	// systemctl is not on the PATH: there is no user systemd here either, and the
	// command never ran.
	return !errors.Is(err, exec.ErrNotFound)
}
