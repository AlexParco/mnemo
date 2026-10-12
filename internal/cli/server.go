package cli

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"runtime"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/service"
	"github.com/spf13/cobra"
)

// `mnemo server` is the machine that stays on: the one whose memory and mailbox
// the others reach.
//
// Everything here is about the service, because a server that only runs while
// somebody is logged in is not a server. What the commands themselves do is
// small: make a token, write it, install, wait for an answer, print the code.

// DefaultPort is where a server listens. It is on loopback, so the only way it
// clashes with anything is another mnemo on the same machine.
const DefaultPort = 7433

func serverCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "server",
		Short: "The machine that stays on, which other machines reach",
		Long: "A server is a mnemo that keeps the memory and answers for it over an SSH tunnel. It runs as a " +
			"service, so it is there after a reboot and while nobody is logged in.",
	}
	command.AddCommand(serverSetupCommand(), serverRestartCommand(),
		serverRotateCommand(), serverRemoveCommand())
	return command
}

func serverSetupCommand() *cobra.Command {
	var port int
	command := &cobra.Command{
		Use:   "setup",
		Short: "Install and start the server, and print the pairing code once",
		Long: "It makes a token, writes it to the config, installs the service for this platform, waits for " +
			"the server to answer, and prints the pairing code. The code carries the token, so it is " +
			"printed once and never stored anywhere but the other machine's config.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return setupServer(cmd, port)
		},
	}
	command.Flags().IntVar(&port, "port", DefaultPort, "the loopback port to listen on")
	return command
}

func setupServer(cmd *cobra.Command, port int) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("mnemo cannot install a service on Windows. `mnemo serve --http` still works " +
			"when you run it yourself, with a port and a token in the config file")
	}
	resolved, err := settings()
	if err != nil {
		return err
	}
	machine, err := resolved.RequireMachine()
	if err != nil {
		return err
	}
	if _, err := resolved.RequireStore(); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	manager := service.New(resolved, mnemoPath())

	// Already set up: say so and say what is running, and do not print the code
	// again. The code carries the token, and a token printed twice is a token in
	// two scrollbacks.
	if state := manager.State(); state.Installed {
		lines := []string{"a server is already set up here: " + state.Path}
		if state.RunningKnown {
			lines = append(lines, "it is "+runningWord(state.Running))
		}
		lines = append(lines,
			"mnemo server rotate prints a new pairing code and stops the old one",
			"mnemo server remove takes the service away; the store and the mailbox stay")
		return say(out, lines)
	}

	if err := portFree(port); err != nil {
		return err
	}
	token, err := newToken()
	if err != nil {
		return err
	}

	// Written before the service starts, because the service reads it: it has
	// none of this shell's environment, so the config file is the only way to
	// tell it anything.
	file, err := config.Load()
	if err != nil {
		return err
	}
	file.Server.Port, file.Server.Token = port, token
	if err := file.Save(); err != nil {
		return err
	}

	installed, err := manager.Install()
	if err != nil {
		return err
	}
	if err := say(out, []string{installed.String()}); err != nil {
		return err
	}
	if installed.Outcome == service.Refused {
		// The token is in the config and nothing is running. Said plainly,
		// because the next command somebody runs decides which of those two
		// they fix.
		return fmt.Errorf("the service was not installed, so nothing is serving yet. The port and the " +
			"token are in the config file: fix what is above and run mnemo server setup again")
	}

	healthy := manager.WaitHealthy(port, token, service.HealthTimeout)
	if err := say(out, []string{healthy.String()}); err != nil {
		return err
	}
	if healthy.Outcome == service.Refused {
		return fmt.Errorf("the service is installed but it did not answer, so do not hand out a pairing " +
			"code yet: a client would save a token for something that is not there")
	}
	return printCode(out, pairing{Machine: machine, Port: port, Token: token})
}

func serverRestartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the server with the same token",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, running, err := hostedServer()
			if err != nil {
				return err
			}
			result, err := service.New(resolved, mnemoPath()).Restart()
			if err != nil {
				return err
			}
			lines := []string{result.String()}
			if result.Outcome != service.Refused {
				// Checked rather than assumed: a service that was restarted and
				// did not come back is the one case where saying nothing would
				// leave every connected machine failing with no explanation.
				lines = append(lines, service.New(resolved, mnemoPath()).
					WaitHealthy(running.Port, running.Token, service.HealthTimeout).String())
			}
			return say(cmd.OutOrStdout(), lines)
		},
	}
}

func serverRotateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate",
		Short: "New token and pairing code; connected machines must connect again",
		Long: "The token is replaced, the service restarts with it, and the new pairing code is printed " +
			"once. Every machine that was connected gets a 401 until it runs mnemo connect with the new " +
			"code, which is the point: rotating is how a token that got out is taken back.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, _, err := hostedServer()
			if err != nil {
				return err
			}
			machine, err := resolved.RequireMachine()
			if err != nil {
				return err
			}
			token, err := newToken()
			if err != nil {
				return err
			}

			file, err := config.Load()
			if err != nil {
				return err
			}
			port := file.Server.Port
			file.Server.Token = token
			if err := file.Save(); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			manager := service.New(resolved, mnemoPath())
			result, err := manager.Restart()
			if err != nil {
				return err
			}
			if err := say(out, []string{result.String()}); err != nil {
				return err
			}
			// The new token is in the config either way, so the code is printed
			// even when the restart needs a hand: without it the only copy of
			// the token would be in a file the other machine cannot read.
			if err := say(out, []string{manager.WaitHealthy(port, token, service.HealthTimeout).String()}); err != nil {
				return err
			}
			return printCode(out, pairing{Machine: machine, Port: port, Token: token})
		},
	}
}

func serverRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Stop and remove the service; the store and the mailbox stay",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := settings()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			result, err := service.New(resolved, mnemoPath()).Remove()
			if err != nil {
				return err
			}
			if err := say(out, []string{result.String()}); err != nil {
				return err
			}

			// The section goes last: while it is there, `serve --http` still
			// works by hand, which is what somebody whose service removal half
			// failed needs.
			file, err := config.Load()
			if err != nil {
				return err
			}
			file.Server.Port, file.Server.Token = 0, ""
			if err := file.Save(); err != nil {
				return err
			}
			return say(out, []string{
				"the server section is out of the config; the store and the mailbox are untouched",
				"this machine works on its own files again, as it did before",
			})
		},
	}
}

// hostedServer is the settings and the server they describe, or the refusal for
// a machine that has none.
func hostedServer() (config.Settings, config.Hosted, error) {
	resolved, err := settings()
	if err != nil {
		return resolved, config.Hosted{}, err
	}
	if !resolved.Server.Set() {
		return resolved, config.Hosted{}, fmt.Errorf("this machine has no server to work on. Set one up " +
			"with: mnemo server setup")
	}
	return resolved, resolved.Server, nil
}

// newToken is 32 bytes from the operating system, in base64url without padding.
//
// Nothing about it is derived from anything: a token that could be worked out
// from a machine's name or the time it was set up would be a token somebody else
// can work out too.
func newToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("this machine's source of randomness is not working, so mnemo will not "+
			"invent a token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// portFree refuses a port something else is on, before anything is written.
//
// Loopback only, which is where the server listens: a port in use elsewhere on
// the machine is not in the way.
func portFree(port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("something is already listening on 127.0.0.1:%d, so mnemo cannot serve there. "+
			"If it is another mnemo, it is already set up; otherwise choose another with --port", port)
	}
	return listener.Close()
}

// mnemoPath is the absolute path of this binary, which is what a unit file and a
// plist have to name: a service has no PATH worth relying on, and `mnemo` by
// name would be found later and be a different binary.
func mnemoPath() string {
	path, err := os.Executable()
	if err != nil {
		return "mnemo"
	}
	return path
}

func runningWord(running bool) string {
	if running {
		return "running"
	}
	return "not running"
}
