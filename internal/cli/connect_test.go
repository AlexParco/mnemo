package cli

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Connecting is tested against a real server with a real forward, because what
// it decides is whether anything is saved — and it decides it from what comes
// back through that forward.
//
// The forward is made by the test rather than by ssh: a tunnel is a listener on
// this end that dials the other, which is the whole of what `connect` depends
// on. The fake ssh only has to exist and stay alive, which is what the keeper
// watches for.
func TestConnectSavesNothingUntilTheServerHasAnswered(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a shell script")
	}
	server, token := serverOnLoopback(t)
	client, env := machine(t, false)
	onPath(t, env, fakeSSH(t, client))

	// The code names the server's port. Which port this end listens on is
	// `connect`'s choice, and the stand-in ssh forwards whichever one it is
	// told to — which is also what proves the -L it was given is right.
	code := pairing{Machine: "vps", Port: server, Token: token}.code()

	// A token the server does not know is refused at once, and nothing is
	// saved: waiting would only delay the same answer.
	wrong := pairing{Machine: "vps", Port: server, Token: "not-the-servers-token"}.code()
	started := time.Now()
	out, exit := runMnemoWith(t, env, "", "connect", wrong, "--ssh", "me@vps")
	if exit != 1 {
		t.Fatalf("a wrong token exited %d:\n%s", exit, out)
	}
	// At once, not after the timeout. The server answered — it said no — and
	// trying for another fifteen seconds would only delay the same answer. The
	// bound is generous because this is a clock, but it is well under the
	// timeout: without the check it takes the whole of it.
	if took := time.Since(started); took > readyWait-5*time.Second {
		t.Errorf("a refusal that waiting cannot fix took %s, nearly the whole %s timeout", took, readyWait)
	}
	if !strings.Contains(out, "mnemo connect") || !strings.Contains(out, "rotated") {
		t.Errorf("the refusal does not say what happened:\n%s", out)
	}
	if saved := configOf(t, client); strings.Contains(saved, "connected = true") {
		t.Errorf("a connection was saved although the server refused the token:\n%s", saved)
	}

	// With the right code it connects, and only then is anything written.
	out, exit = runMnemoWith(t, env, "", "connect", code, "--ssh", "me@vps")
	if exit != 0 {
		t.Fatalf("connect exited %d:\n%s\nconfig:\n%s", exit, out, configOf(t, client))
	}
	for _, want := range []string{"Connected to vps through me@vps", "restart open chats"} {
		if !strings.Contains(out, want) {
			t.Errorf("it does not say %q:\n%s", want, out)
		}
	}
	saved := configOf(t, client)
	for _, want := range []string{`machine = "vps"`, `ssh = "me@vps"`, "connected = true",
		fmt.Sprintf("server_port = %d", server)} {
		if !strings.Contains(saved, want) {
			t.Errorf("the config does not carry %q:\n%s", want, saved)
		}
	}
	// The mode every other command reads says the same thing.
	if shown, _ := runMnemo(t, env, "config"); !strings.Contains(shown, "connected to vps via me@vps") {
		t.Errorf("mnemo config says:\n%s", shown)
	}

	// Disconnecting keeps the entry, because `mnemo connect` with no code
	// returns to it, and says plainly which files are in use again.
	out, exit = runMnemo(t, env, "disconnect")
	if exit != 0 {
		t.Fatalf("disconnect exited %d:\n%s", exit, out)
	}
	if !strings.Contains(out, "Local mode") {
		t.Errorf("disconnect says:\n%s", out)
	}
	saved = configOf(t, client)
	if !strings.Contains(saved, "connected = false") || !strings.Contains(saved, `machine = "vps"`) {
		t.Errorf("after disconnect the config is:\n%s", saved)
	}
	if shown, _ := runMnemo(t, env, "config"); !strings.Contains(shown, "disconnected from vps") {
		t.Errorf("mnemo config says:\n%s", shown)
	}

	// And connect with no code returns to it.
	out, exit = runMnemoWith(t, env, "", "connect")
	if exit != 0 {
		t.Fatalf("reconnecting exited %d:\n%s", exit, out)
	}
	if !strings.Contains(configOf(t, client), "connected = true") {
		t.Errorf("reconnecting did not save it:\n%s", configOf(t, client))
	}
}

// A machine that runs a server is the one others connect to, and a server
// pointed at another server would answer for files it is not looking at.
func TestAServerMachineWillNotConnectToAnother(t *testing.T) {
	home, env := machine(t, false)
	write(t, filepath.Join(home, "config", "mnemo", "config.toml"),
		"[server]\nport = 7433\ntoken = \"its-own\"\n")

	code := pairing{Machine: "elsewhere", Port: 7433, Token: "theirs"}.code()
	out, exit := runMnemoWith(t, env, "", "connect", code, "--ssh", "me@elsewhere")
	if exit != 1 {
		t.Fatalf("it exited %d:\n%s", exit, out)
	}
	if !strings.Contains(out, "mnemo server remove") {
		t.Errorf("the refusal does not say what to do:\n%s", out)
	}
}

// Without a saved connection there is nothing to reopen, and the answer says
// where a code comes from.
func TestConnectWithNoCodeAndNothingSavedSaysWhereToGetOne(t *testing.T) {
	_, env := machine(t, false)
	out, exit := runMnemoWith(t, env, "", "connect")
	if exit != 2 {
		t.Errorf("it exited %d:\n%s", exit, out)
	}
	if !strings.Contains(out, "mnemo server setup") {
		t.Errorf("it says:\n%s", out)
	}
}

// serverOnLoopback starts a real mnemo server and returns its port and token.
func serverOnLoopback(t *testing.T) (int, string) {
	t.Helper()
	home, env := machine(t, true)
	const token = "the-servers-token-for-this-test"
	port := freePortFor(t)
	write(t, filepath.Join(home, "config", "mnemo", "config.toml"),
		fmt.Sprintf("[server]\nport = %d\ntoken = %q\n", port, token))

	command := exec.Command(built(t), "serve", "--http")
	command.Env = append(env, "MNEMO_MACHINE=vps")
	said := &complaints{}
	command.Stderr = said
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	waitForPort(t, port, said)
	return port, token
}

// fakeSSH is the ssh stand-in from testdata, built once and put in a directory
// of its own so it can go first on the PATH.
//
// It really forwards: it reads the -L the keeper passes and listens on this end,
// which is the whole of what mnemo depends on ssh for. A script that only slept
// would prove the keeper starts something and nothing about whether the tunnel
// it asked for is the tunnel it got.
func fakeSSH(t *testing.T, home string) string {
	t.Helper()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	built, err := standInSSH()
	if err != nil {
		t.Fatalf("building the ssh stand-in: %v", err)
	}
	// Copied rather than linked: the PATH entry has to be called `ssh`, and the
	// one build is shared by every test that needs one.
	content, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bin, "ssh")
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// standInSSH builds testdata/fakessh once for the whole package.
var standInSSH = sync.OnceValues(func() (string, error) {
	directory, err := os.MkdirTemp("", "mnemo-fakessh-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, "fakessh")
	build := exec.Command("go", "build", "-o", path, "./testdata/fakessh")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return "", err
	}
	return path, nil
})

// onPath puts a directory first on the PATH of an environment, in place.
func onPath(t *testing.T, env []string, directory string) {
	t.Helper()
	for i, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			env[i] = "PATH=" + directory + string(os.PathListSeparator) + os.Getenv("PATH")
			return
		}
	}
	t.Fatal("the environment has no PATH")
}

func configOf(t *testing.T, home string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(home, "config", "mnemo", "config.toml"))
	if err != nil {
		return ""
	}
	return string(content)
}

func waitForPort(t *testing.T, port int, said *complaints) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			connection.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("nothing is listening on %d\nstderr:\n%s", port, said)
}
