package service

import (
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Nothing in this file runs systemd, launchd or sudo, and nothing in it may: the
// suite has to pass on a machine that has none of them. Every command goes
// through the recorder below, which keeps what it was asked to run and answers
// the way the real tool would.

type recorder struct {
	ran []string
	// plist is what was written to the temporary file `sudo install` was given,
	// read at the moment it was asked for, because the file is deleted as soon as
	// installing returns.
	plist string
	// answer gives the output and the error for one command line. A nil answer
	// means every command succeeds silently.
	answer func(line string) (string, error)
}

func (r *recorder) run(name string, args ...string) (string, error) {
	// The temporary plist's name is random, so it is replaced by a fixed marker:
	// the test is about the command, not about mkstemp.
	shown := append([]string{name}, args...)
	if name == "sudo" && len(args) > 2 && args[0] == "install" {
		source := len(shown) - 2
		if content, err := os.ReadFile(shown[source]); err == nil {
			r.plist = string(content)
		}
		shown[source] = "<tmp>"
	}
	line := strings.Join(shown, " ")
	r.ran = append(r.ran, line)
	if r.answer == nil {
		return "", nil
	}
	return r.answer(line)
}

// linuxManager is a server on a Linux machine whose home is a temporary
// directory, with lingering on and a session bus present.
func linuxManager(t *testing.T) (*Manager, *recorder) {
	t.Helper()
	home := t.TempDir()
	rec := &recorder{answer: func(line string) (string, error) {
		if strings.HasPrefix(line, "loginctl ") {
			return "Linger=yes\n", nil
		}
		return "", nil
	}}
	return &Manager{
		Platform:   Linux,
		User:       "ada",
		Home:       home,
		ConfigHome: filepath.Join(home, ".config"),
		Logs:       filepath.Join(home, ".local", "state", "mnemo", "logs"),
		Binary:     "/usr/local/bin/mnemo",
		Run:        rec.run,
	}, rec
}

// macManager is a server on a macOS machine, with the LaunchDaemons directory
// pointed at a temporary one so that nothing needs root.
func macManager(t *testing.T) (*Manager, *recorder) {
	t.Helper()
	home := t.TempDir()
	rec := &recorder{}
	return &Manager{
		Platform:      MacOS,
		User:          "ada",
		Home:          home,
		ConfigHome:    filepath.Join(home, ".config"),
		Logs:          filepath.Join(home, ".local", "state", "mnemo", "logs"),
		LaunchDaemons: filepath.Join(t.TempDir(), "LaunchDaemons"),
		Binary:        "/usr/local/bin/mnemo",
		Run:           rec.run,
	}, rec
}

func wantRan(t *testing.T, rec *recorder, want ...string) {
	t.Helper()
	if strings.Join(rec.ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands run:\n got:\n  %s\nwant:\n  %s",
			strings.Join(rec.ran, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestOnLinuxTheUnitFileIsTheOneTheSpecGives(t *testing.T) {
	manager, _ := linuxManager(t)
	if _, err := manager.Install(); err != nil {
		t.Fatalf("installing: %v", err)
	}

	path := filepath.Join(manager.ConfigHome, "systemd", "user", "mnemo.service")
	if manager.UnitPath() != path {
		t.Errorf("the unit path is %s, want %s", manager.UnitPath(), path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the unit file: %v", err)
	}
	want := "[Unit]\n" +
		"Description=mnemo server\n" +
		"After=network.target\n" +
		"\n" +
		"[Service]\n" +
		"ExecStart=/usr/local/bin/mnemo serve --http\n" +
		"Restart=on-failure\n" +
		"RestartSec=2\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
	if string(content) != want {
		t.Errorf("the unit file is:\n%q\nwant:\n%q", content, want)
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm() != 0o644 {
		t.Errorf("the unit file's mode is %o, want 644", info.Mode().Perm())
	}
}

func TestOnLinuxItRunsTheCommandsInTheOrderTheSpecGives(t *testing.T) {
	manager, rec := linuxManager(t)
	result, err := manager.Install()
	if err != nil {
		t.Fatalf("installing: %v", err)
	}
	if result.Outcome != Installed {
		t.Errorf("the outcome is %q: %s", result.Outcome, result)
	}
	if result.Advice != "" {
		t.Errorf("with lingering on there is nothing to advise, got: %s", result.Advice)
	}
	wantRan(t, rec,
		"systemctl --user show-environment",
		"systemctl --user daemon-reload",
		"systemctl --user enable --now mnemo.service",
		"loginctl show-user ada --property=Linger",
	)
}

func TestWithoutLingeringItInstallsAndWarns(t *testing.T) {
	manager, rec := linuxManager(t)
	rec.answer = func(line string) (string, error) {
		if strings.HasPrefix(line, "loginctl ") {
			return "Linger=no\n", nil
		}
		return "", nil
	}

	result, err := manager.Install()
	if err != nil {
		t.Fatalf("installing: %v", err)
	}
	if result.Outcome != Installed {
		t.Fatalf("the outcome is %q, want it installed anyway: %s", result.Outcome, result)
	}
	if !exists(manager.UnitPath()) {
		t.Error("the unit file was not written")
	}
	if !strings.Contains(result.Advice, "sudo loginctl enable-linger ada") {
		t.Errorf("the warning does not name the command to type:\n%s", result.Advice)
	}
	for _, line := range rec.ran {
		if strings.HasPrefix(line, "sudo") {
			t.Errorf("mnemo never runs sudo on Linux, and it ran %q", line)
		}
	}
}

func TestWhenLingeringCannotBeReadItWarnsAllTheSame(t *testing.T) {
	manager, rec := linuxManager(t)
	rec.answer = func(line string) (string, error) {
		if strings.HasPrefix(line, "loginctl ") {
			return "Failed to get user: No such user\n", errors.New("exit status 1")
		}
		return "", nil
	}

	result, _ := manager.Install()
	if result.Outcome != Installed || !strings.Contains(result.Advice, "sudo loginctl enable-linger ada") {
		t.Errorf("an unreadable Linger must be treated as off: %s", result)
	}
}

func TestWithoutASessionBusItStopsAndInstallsNothing(t *testing.T) {
	manager, rec := linuxManager(t)
	rec.answer = func(line string) (string, error) {
		if line == "systemctl --user show-environment" {
			return "Failed to connect to bus: No medium found\n", errors.New("exit status 1")
		}
		return "", nil
	}

	result, err := manager.Install()
	if err != nil {
		t.Fatalf("installing: %v", err)
	}
	if result.Outcome != Refused {
		t.Fatalf("the outcome is %q, want a refusal: %s", result.Outcome, result)
	}
	if exists(manager.UnitPath()) {
		t.Error("a refusal wrote a unit file")
	}
	wantRan(t, rec, "systemctl --user show-environment")
	for _, phrase := range []string{"systemd session bus", "Nothing was installed", "mnemo serve --http"} {
		if !strings.Contains(result.String(), phrase) {
			t.Errorf("the refusal does not say %q:\n%s", phrase, result)
		}
	}
}

func TestWhenSystemctlIsNotEvenInstalledItStops(t *testing.T) {
	manager, rec := linuxManager(t)
	// The error a missing program really gives, built rather than run: it is the
	// one hasBus has to recognise, and nothing has to be executed to produce it.
	rec.answer = func(string) (string, error) {
		return "", &exec.Error{Name: "systemctl", Err: exec.ErrNotFound}
	}

	if result, _ := manager.Install(); result.Outcome != Refused || exists(manager.UnitPath()) {
		t.Errorf("a machine without systemctl must be refused: %s", result)
	}
}

func TestOnLinuxRestartRunsWhatTheSpecSays(t *testing.T) {
	manager, rec := linuxManager(t)
	result, err := manager.Restart()
	if err != nil || result.Outcome != Restarted {
		t.Fatalf("restarting: %v, %s", err, result)
	}
	wantRan(t, rec, "systemctl --user restart mnemo.service")
}

func TestOnLinuxRemoveRunsWhatTheSpecSaysInOrder(t *testing.T) {
	manager, rec := linuxManager(t)
	if _, err := manager.Install(); err != nil {
		t.Fatalf("installing: %v", err)
	}
	rec.ran = nil

	result, err := manager.Remove()
	if err != nil {
		t.Fatalf("removing: %v", err)
	}
	if result.Outcome != Removed {
		t.Fatalf("the outcome is %q: %s", result.Outcome, result)
	}
	wantRan(t, rec,
		"systemctl --user disable --now mnemo.service",
		"systemctl --user daemon-reload",
	)
	if exists(manager.UnitPath()) {
		t.Error("the unit file is still there")
	}
}

func TestRemovingWhatWasNeverInstalledDoesNothing(t *testing.T) {
	linux, linuxRec := linuxManager(t)
	result, err := linux.Remove()
	if err != nil || result.Outcome != Nothing {
		t.Errorf("on Linux: %v, %s", err, result)
	}
	wantRan(t, linuxRec)

	mac, macRec := macManager(t)
	result, err = mac.Remove()
	if err != nil || result.Outcome != Nothing {
		t.Errorf("on macOS: %v, %s", err, result)
	}
	wantRan(t, macRec)
}

func TestOnLinuxStateReportsTheUnitAndWhetherItRuns(t *testing.T) {
	manager, rec := linuxManager(t)
	if state := manager.State(); state.Installed {
		t.Errorf("nothing is installed yet: %+v", state)
	}
	if _, err := manager.Install(); err != nil {
		t.Fatalf("installing: %v", err)
	}

	rec.answer = func(string) (string, error) { return "active\n", nil }
	state := manager.State()
	if !state.Installed || state.Path != manager.UnitPath() || !state.RunningKnown || !state.Running {
		t.Errorf("state is %+v, want it installed and running", state)
	}

	rec.answer = func(string) (string, error) { return "inactive\n", errors.New("exit status 3") }
	if state := manager.State(); !state.RunningKnown || state.Running {
		t.Errorf("state is %+v, want it known and stopped", state)
	}

	rec.answer = func(string) (string, error) {
		return "Failed to connect to bus: No medium found\n", errors.New("exit status 1")
	}
	if state := manager.State(); state.RunningKnown {
		t.Errorf("state is %+v, want running unknown without a bus", state)
	}
}

func TestOnMacOSThePlistHasEveryKeyTheSpecLists(t *testing.T) {
	manager, _ := macManager(t)
	plist := manager.Plist()

	log := filepath.Join(manager.Logs, "server.log")
	for _, want := range []string{
		"<key>Label</key>\n\t<string>io.github.alexparco.mnemo</string>",
		"<key>UserName</key>\n\t<string>ada</string>",
		"<key>ProgramArguments</key>\n\t<array>\n\t\t<string>/usr/local/bin/mnemo</string>\n" +
			"\t\t<string>serve</string>\n\t\t<string>--http</string>\n\t</array>",
		"<key>EnvironmentVariables</key>\n\t<dict>\n\t\t<key>HOME</key>\n\t\t<string>" + manager.Home +
			"</string>\n\t\t<key>PATH</key>\n\t\t<string>" + ServicePath + "</string>\n\t</dict>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<key>StandardOutPath</key>\n\t<string>" + log + "</string>",
		"<key>StandardErrorPath</key>\n\t<string>" + log + "</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("the plist does not contain:\n%s\n\ngot:\n%s", want, plist)
		}
	}
	if !strings.HasPrefix(plist, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC") {
		t.Errorf("the plist does not begin as a plist does:\n%s", plist)
	}
	if ServicePath != "/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin" {
		t.Errorf("the service's PATH is %q, which is not the one the spec gives", ServicePath)
	}
	// A LaunchAgent would be ~/Library/LaunchAgents, and the spec says why it
	// will not do.
	if !strings.HasSuffix(manager.PlistPath(), "/io.github.alexparco.mnemo.plist") {
		t.Errorf("the plist is at %s", manager.PlistPath())
	}
	if LaunchDaemons != "/Library/LaunchDaemons" {
		t.Errorf("plists are installed into %s, not the system's LaunchDaemons", LaunchDaemons)
	}
}

func TestThePlistEscapesWhatWouldBreakTheXML(t *testing.T) {
	manager, _ := macManager(t)
	manager.Home = "/Users/ada & co"
	if plist := manager.Plist(); !strings.Contains(plist, "<string>/Users/ada &amp; co</string>") {
		t.Errorf("an ampersand in a path reached the plist unescaped:\n%s", plist)
	}
}

func TestOnMacOSItInstallsThePlistAsRootAndBootstrapsIt(t *testing.T) {
	manager, rec := macManager(t)
	result, err := manager.Install()
	if err != nil {
		t.Fatalf("installing: %v", err)
	}
	if result.Outcome != Installed {
		t.Fatalf("the outcome is %q: %s", result.Outcome, result)
	}
	wantRan(t, rec,
		"sudo install -m 644 -o root -g wheel <tmp> "+manager.PlistPath(),
		"sudo launchctl bootstrap system "+manager.PlistPath(),
	)
	if rec.plist != manager.Plist() {
		t.Errorf("the file handed to install is not the plist:\n%s", rec.plist)
	}
	// launchd opens the daemon's output files itself and will not make the
	// directory for them.
	if !exists(manager.Logs) {
		t.Errorf("the log directory %s was not created", manager.Logs)
	}
}

func TestOnMacOSAFailedSudoInstallsNothingAndSaysWhy(t *testing.T) {
	manager, rec := macManager(t)
	rec.answer = func(line string) (string, error) {
		if strings.HasPrefix(line, "sudo install") {
			return "sudo: a password is required\n", errors.New("exit status 1")
		}
		return "", nil
	}

	result, _ := manager.Install()
	if result.Outcome != Refused {
		t.Fatalf("the outcome is %q, want a refusal: %s", result.Outcome, result)
	}
	if len(rec.ran) != 1 {
		t.Errorf("it went on after sudo failed: %v", rec.ran)
	}
	if !strings.Contains(result.String(), "a password is required") {
		t.Errorf("the refusal drops sudo's own words:\n%s", result)
	}
}

func TestOnMacOSASecondSetupSaysToRestart(t *testing.T) {
	manager, rec := macManager(t)
	rec.answer = func(line string) (string, error) {
		if strings.Contains(line, "bootstrap") {
			return "Bootstrap failed: service already loaded\n", errors.New("exit status 5")
		}
		return "", nil
	}

	result, _ := manager.Install()
	if result.Outcome != Installed || !strings.Contains(result.Advice, "mnemo server restart") {
		t.Errorf("an already loaded service must still count as installed: %s", result)
	}
}

func TestOnMacOSRestartRunsWhatTheSpecSays(t *testing.T) {
	manager, rec := macManager(t)
	result, err := manager.Restart()
	if err != nil || result.Outcome != Restarted {
		t.Fatalf("restarting: %v, %s", err, result)
	}
	wantRan(t, rec, "sudo launchctl kickstart -k system/io.github.alexparco.mnemo")
}

func TestOnMacOSRemoveBootsItOutAndThenDeletesThePlist(t *testing.T) {
	manager, rec := macManager(t)
	if _, err := manager.Install(); err != nil {
		t.Fatalf("installing: %v", err)
	}
	// `sudo install` is the stub, so the file it would have copied is put there
	// by hand: remove has to find a plist to remove.
	if err := os.MkdirAll(filepath.Dir(manager.PlistPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.PlistPath(), []byte(manager.Plist()), 0o644); err != nil {
		t.Fatal(err)
	}
	rec.ran = nil

	result, err := manager.Remove()
	if err != nil {
		t.Fatalf("removing: %v", err)
	}
	if result.Outcome != Removed {
		t.Fatalf("the outcome is %q: %s", result.Outcome, result)
	}
	wantRan(t, rec,
		"sudo launchctl bootout system/io.github.alexparco.mnemo",
		"sudo rm -f "+manager.PlistPath(),
	)
}

func TestOnMacOSRemoveGoesOnWhenTheServiceWasNotLoaded(t *testing.T) {
	manager, rec := macManager(t)
	if err := os.MkdirAll(filepath.Dir(manager.PlistPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.PlistPath(), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec.answer = func(line string) (string, error) {
		if strings.Contains(line, "bootout") {
			return "Boot-out failed: 3: No such process\n", errors.New("exit status 3")
		}
		return "", nil
	}

	result, _ := manager.Remove()
	if result.Outcome != Removed {
		t.Errorf("a plist that was never loaded still has to go: %s", result)
	}
	wantRan(t, rec,
		"sudo launchctl bootout system/io.github.alexparco.mnemo",
		"sudo rm -f "+manager.PlistPath(),
	)
}

func TestOnWindowsEverythingIsRefusedAndSaysHowToRunByHand(t *testing.T) {
	rec := &recorder{}
	manager := &Manager{Platform: Windows, User: "ada", Home: `C:\Users\ada`, Run: rec.run}

	for name, call := range map[string]func() (Result, error){
		"install": manager.Install,
		"restart": manager.Restart,
		"remove":  manager.Remove,
	} {
		result, err := call()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.Outcome != Refused {
			t.Errorf("%s: the outcome is %q, want a refusal", name, result.Outcome)
		}
		if !strings.Contains(result.String(), "mnemo serve --http") {
			t.Errorf("%s: the refusal does not say the server can still be run by hand:\n%s", name, result)
		}
	}
	wantRan(t, rec)
	if state := manager.State(); state.Installed || state.RunningKnown {
		t.Errorf("state on Windows is %+v, want nothing claimed", state)
	}
}

func TestTheClaimedPlatformIsWhatDecides(t *testing.T) {
	// The platform is a field so that this machine, whatever it is, exercises
	// both. If it were read from runtime.GOOS at the point of use, half of this
	// file could never run.
	manager, rec := linuxManager(t)
	manager.Platform = MacOS
	manager.LaunchDaemons = filepath.Join(t.TempDir(), "LaunchDaemons")
	if _, err := manager.Install(); err != nil {
		t.Fatalf("installing: %v", err)
	}
	if len(rec.ran) == 0 || !strings.HasPrefix(rec.ran[0], "sudo install") {
		t.Errorf("a manager claiming macOS ran %v", rec.ran)
	}
	if exists(manager.UnitPath()) {
		t.Error("it wrote a systemd unit on macOS")
	}
	switch got := CurrentPlatform(); got {
	case Linux, MacOS, Windows:
	default:
		t.Errorf("this machine's platform is %q, which is not one of the three", got)
	}
}

// health, against a real loopback server: no systemd and no network, but the
// request, the header and the status are the real ones.

func healthServer(t *testing.T, token string) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mnemo"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"version":"0.5.0","api":1,"machine":"vps"}`))
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return listener.Addr().(*net.TCPAddr).Port
}

func TestItWaitsForTheServerToAnswerWithTheToken(t *testing.T) {
	manager, _ := linuxManager(t)
	port := healthServer(t, "the-token")

	result := manager.WaitHealthy(port, "the-token", HealthTimeout)
	if result.Outcome != Running {
		t.Errorf("the outcome is %q: %s", result.Outcome, result)
	}
	if !strings.Contains(result.Detail, strconv.Itoa(port)) {
		t.Errorf("the detail does not name the address: %s", result.Detail)
	}
}

func TestAServerThatRejectsTheTokenIsReportedAtOnce(t *testing.T) {
	manager, _ := linuxManager(t)
	port := healthServer(t, "another-token")

	started := time.Now()
	result := manager.WaitHealthy(port, "the-token", HealthTimeout)
	if result.Outcome != Refused || !strings.Contains(result.Detail, "rejected the token") {
		t.Fatalf("the outcome is %q: %s", result.Outcome, result)
	}
	if !strings.Contains(result.Advice, "mnemo server restart") {
		t.Errorf("the refusal does not say what to do:\n%s", result.Advice)
	}
	if time.Since(started) > 2*time.Second {
		t.Errorf("it waited %s for an answer that will not change", time.Since(started))
	}
}

func TestWhenNothingAnswersItSaysWhereToReadWhy(t *testing.T) {
	linux, _ := linuxManager(t)
	// Nothing is listening on this port: the listener is closed before it is used.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	result := linux.WaitHealthy(port, "the-token", 10*time.Millisecond)
	if result.Outcome != Refused {
		t.Fatalf("the outcome is %q: %s", result.Outcome, result)
	}
	if !strings.Contains(result.Advice, "journalctl --user -u mnemo.service") {
		t.Errorf("on Linux it must point at the journal:\n%s", result.Advice)
	}

	mac, _ := macManager(t)
	result = mac.WaitHealthy(port, "the-token", 10*time.Millisecond)
	if !strings.Contains(result.Advice, filepath.Join(mac.Logs, "server.log")) {
		t.Errorf("on macOS it must point at the daemon's log file:\n%s", result.Advice)
	}
}
