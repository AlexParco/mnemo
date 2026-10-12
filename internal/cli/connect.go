package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/mailbox"
	"github.com/AlexParco/mnemo/internal/remote"
	"github.com/AlexParco/mnemo/internal/tunnel"
	"github.com/spf13/cobra"
)

// `mnemo connect` and `mnemo disconnect`: which machine's files this one works
// on.
//
// Nothing is saved until the whole of it has worked. A saved remote that does
// not answer is worse than no remote at all: every chat opened afterwards
// refuses to fall back to local files, which is right, so a machine would be
// left with neither its own memory nor anybody else's.

// readyWait is how long `connect` gives the tunnel to come up before it gives
// up. Longer than the on-demand check, because this one includes ssh
// authenticating for the first time.
const readyWait = 15 * time.Second

func connectCommand() *cobra.Command {
	var sshTarget string
	var yes bool
	command := &cobra.Command{
		Use:   "connect [code]",
		Short: "Use another machine's memory, through an SSH tunnel",
		Long: "With a pairing code, this machine is wired to the server the code names: it asks for the SSH " +
			"connection, opens the tunnel, checks the server answers, and only then saves anything.\n\n" +
			"With no code it reopens what was saved. Chats opened from then on use the server; chats that " +
			"are already open keep the files they started with.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usage("this takes one pairing code, and %d were given", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			given := ""
			if len(args) == 1 {
				given = args[0]
			}
			return connectTo(cmd, given, sshTarget, yes)
		},
	}
	command.Flags().StringVar(&sshTarget, "ssh", "",
		"the SSH connection to the server: user@host, or an alias from ~/.ssh/config")
	command.Flags().BoolVar(&yes, "yes", false, "replace a saved connection to another server without asking")
	return command
}

func connectTo(cmd *cobra.Command, given, sshTarget string, yes bool) error {
	resolved, err := settings()
	if err != nil {
		return err
	}
	machine, err := resolved.RequireMachine()
	if err != nil {
		return err
	}
	// A server never connects: its files are the ones everybody else reaches,
	// and a server pointed at another server would answer for files it is not
	// looking at.
	if resolved.Server.Set() {
		return fmt.Errorf("this machine runs a server, so it is the one others connect to. Remove it with " +
			"mnemo server remove if this machine should use somebody else's memory instead")
	}
	// ssh is checked before anything else, because without it nothing here can
	// work and the failure would otherwise arrive as a tunnel that never opens.
	if _, err := tunnel.SSH(); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	file, err := config.Load()
	if err != nil {
		return err
	}

	wanted, err := connectionFor(cmd, file, given, sshTarget, machine, yes)
	if err != nil {
		return err
	}

	// Saved as connected only at the end. Until then it goes into the keeper
	// through the settings in memory, so nothing on disk points at a server
	// that has not answered.
	trying := resolved
	trying.Connection = &wanted
	keeper := tunnel.New(trying.Paths, tunnel.Options{})

	previous := file.Remotes
	file.Remotes = []config.Remote{remoteOf(wanted)}
	if err := file.Save(); err != nil {
		return err
	}
	undo := func() {
		file.Remotes = previous
		// Nothing can be done about a failure here, and saying so would bury
		// the real reason this command stopped.
		_ = file.Save()
	}

	if err := keeper.Start(); err != nil {
		undo()
		return err
	}
	health, err := waitForServer(cmd, trying, keeper)
	if err != nil {
		undo()
		_ = keeper.Stop(cmd.Context())
		return err
	}

	return say(out, []string{
		fmt.Sprintf("Connected to %s through %s.", health.Machine, wanted.SSH),
		"Chats opened from now on use it; restart open chats to switch them.",
	})
}

// connectionFor works out what to connect to: from a code, or from what was
// saved.
func connectionFor(cmd *cobra.Command, file *config.File, given, sshTarget, machine string,
	yes bool) (config.Connection, error) {
	var wanted config.Connection
	if given == "" {
		// No code: reopen what is there. This is the form somebody runs after a
		// reboot, or after a disconnect.
		if len(file.Remotes) == 0 {
			return wanted, usage("this machine has no saved connection. Run mnemo server setup on the " +
				"machine that keeps the memory, and paste the pairing code it prints here")
		}
		saved := file.Remotes[0]
		saved.Connected = true
		if sshTarget != "" {
			saved.SSH = sshTarget
		}
		return connectionOf(saved), nil
	}

	carried, err := readCode(given)
	if err != nil {
		return wanted, err
	}
	// Two machines with one label would collide on every address: a message to
	// `chat-a@laptop1` would have two places to go.
	if carried.Machine == machine {
		return wanted, fmt.Errorf("that pairing code is for a machine called %q, which is this machine's "+
			"own label. Give one of them another label with: mnemo config set machine <label>", machine)
	}

	// A saved connection to a different server is replaced only on purpose: it
	// is where this machine's chats have been going.
	if len(file.Remotes) > 0 && file.Remotes[0].Machine != carried.Machine && !yes {
		if err := confirmReplacing(cmd, file.Remotes[0].Machine, carried.Machine); err != nil {
			return wanted, err
		}
	}

	target := sshTarget
	if target == "" {
		if target, err = askForSSH(cmd, carried.Machine); err != nil {
			return wanted, err
		}
	}

	local := carried.Port
	if !portIsFree(local) {
		// The server's own port is the obvious choice for this end too, and
		// when it is taken — often by another tunnel — any free one does.
		if local, err = anyFreePort(); err != nil {
			return wanted, err
		}
	}
	return config.Connection{
		Machine: carried.Machine, SSH: target, ServerPort: carried.Port,
		LocalPort: local, Token: carried.Token, Connected: true,
	}, nil
}

// waitForServer is the check that decides whether anything is saved: the tunnel
// is up, the server is the one the code named, and it speaks the same api.
func waitForServer(cmd *cobra.Command, trying config.Settings, keeper *tunnel.Keeper) (remote.Health, error) {
	client := remote.New(*trying.Connection, mailbox.Caller{
		Machine: trying.Machine, Tool: "terminal", Instance: mailbox.Instance(), Via: "remote", PID: os.Getpid(),
	})

	deadline := time.Now().Add(readyWait)
	var last error
	for time.Now().Before(deadline) {
		health, err := client.Reachable(cmd.Context())
		if err == nil {
			return health, nil
		}
		last = err
		// A refusal that waiting cannot fix: a token the server does not know,
		// or an api it does not speak. Trying again for fifteen seconds would
		// only delay the same answer.
		var notSent *remote.NotSent
		if !errors.As(err, &notSent) {
			return health, err
		}
		time.Sleep(250 * time.Millisecond)
	}

	// SSH's own last words are what says whether this is a key, a host or a
	// name — nothing mnemo could work out for itself.
	said := lastLines(keeper.LogFile(), 10)
	if said != "" {
		said = "\n\nWhat ssh said:\n" + said
	}
	return remote.Health{}, fmt.Errorf("the tunnel to %s did not come up in %s, so nothing was saved "+
		"(%v).%s", trying.Connection.Machine, readyWait, last, said)
}

func disconnectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect",
		Short: "Close the tunnel and go back to this machine's own files",
		Long: "The saved connection is kept, so mnemo connect with no code returns to it. This machine's " +
			"own store and mailbox are exactly as they were: nothing is ever merged in either direction.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := settings()
			if err != nil {
				return err
			}
			file, err := config.Load()
			if err != nil {
				return err
			}
			if len(file.Remotes) == 0 {
				return say(cmd.OutOrStdout(), []string{
					"this machine has no connection, so it is already working on its own files",
				})
			}

			// Written before the keeper is stopped, so that a keeper which
			// restarts ssh in the same moment reads it and exits of its own
			// accord. The other order leaves a window where it reconnects.
			for i := range file.Remotes {
				file.Remotes[i].Connected = false
			}
			if err := file.Save(); err != nil {
				return err
			}
			if err := tunnel.New(resolved.Paths, tunnel.Options{}).Stop(cmd.Context()); err != nil {
				return err
			}
			return say(cmd.OutOrStdout(), []string{
				"Local mode. Chats opened from now on use this machine's files; restart open chats to switch them.",
			})
		},
	}
}

func tunnelCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tunnel",
		Short: "Keep the SSH tunnel to the server open",
		Long: "Started on demand by anything that needs the server, never by hand. One per machine: a second " +
			"one finds the lock taken and exits at once.",
		// Hidden, and a contract with everything that starts it: its interface
		// has to keep working across releases.
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := settings()
			if err != nil {
				return err
			}
			return tunnel.New(resolved.Paths, tunnel.Options{}).Run(cmd.Context())
		},
	}
}

// askForSSH asks how to reach the server, because mnemo cannot know: the code
// carries a machine label, and a label is not a host.
func askForSSH(cmd *cobra.Command, machine string) (string, error) {
	if _, err := fmt.Fprintf(cmd.OutOrStdout(),
		"  SSH connection to %s (user@host, or an alias from ~/.ssh/config): ", machine); err != nil {
		return "", err
	}
	typed, err := line(cmd.InOrStdin())
	if err != nil || typed == "" {
		return "", usage("mnemo needs the SSH connection to %s. Give it with --ssh user@host, which is also "+
			"how this works without a terminal.", machine)
	}
	return typed, nil
}

// confirmReplacing asks before a machine's chats are pointed somewhere else.
func confirmReplacing(cmd *cobra.Command, from, to string) error {
	if _, err := fmt.Fprintf(cmd.OutOrStdout(),
		"  this machine is connected to %s. Replace it with %s? Type yes to go ahead: ", from, to); err != nil {
		return err
	}
	typed, err := line(cmd.InOrStdin())
	if err != nil || !strings.EqualFold(typed, "yes") {
		return fmt.Errorf("nothing was changed; this machine still uses %s", from)
	}
	return nil
}

func remoteOf(connection config.Connection) config.Remote {
	return config.Remote{
		Machine: connection.Machine, SSH: connection.SSH, ServerPort: connection.ServerPort,
		LocalPort: connection.LocalPort, Token: connection.Token, Connected: connection.Connected,
	}
}

func connectionOf(saved config.Remote) config.Connection {
	return config.Connection{
		Machine: saved.Machine, SSH: saved.SSH, ServerPort: saved.ServerPort,
		LocalPort: saved.LocalPort, Token: saved.Token, Connected: saved.Connected,
	}
}

func portIsFree(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	return listener.Close() == nil
}

func anyFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("this machine would not give mnemo a port for its end of the tunnel: %w", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// lastLines is the end of a log, for a failure whose reason is in it.
func lastLines(path string, most int) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) > most {
		lines = lines[len(lines)-most:]
	}
	return "    " + strings.Join(lines, "\n    ")
}
