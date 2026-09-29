package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/AlexParco/mnemo/internal/integration"
	"github.com/AlexParco/mnemo/internal/mailbox"
	"github.com/spf13/cobra"
)

// `mnemo run` is what gives a chat a name, and a name is the whole of the
// mailbox's admission control.
//
// It checks everything that can be checked before the tool starts, because the
// alternative is a chat that opens, looks fine, and has no memory or no mailbox —
// and nobody finds out until they need one. Then it replaces its own process with
// the tool, so the terminal and every signal go straight there and mnemo is not a
// layer in between.

func runCommand() *cobra.Command {
	var name string
	command := &cobra.Command{
		Use:   "run <claude|codex|opencode> [directory] [-- tool arguments]",
		Short: "Open a coding tool with a mailbox name",
		Long: "Everything that can be checked is checked first: the name, this machine's label, the " +
			"directory, that mnemo is registered in that tool, that the tool is on the PATH, and that the " +
			"address is free. Then mnemo replaces itself with the tool, so the terminal and signals go " +
			"straight to it.\n\n" +
			"Arguments after -- are passed to the tool.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usage("name the tool to open: %s", strings.Join(integration.Names, ", "))
			}
			if !contains(integration.Names, args[0]) {
				return usage("%q is not a tool mnemo knows. It knows: %s", args[0], strings.Join(integration.Names, ", "))
			}
			if len(args) > 2 {
				return usage("this takes a tool and an optional directory; put the tool's own arguments after --")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			tool := args[0]
			directory := ""
			if len(args) == 2 {
				directory = args[1]
			}
			return open(cmd, tool, name, directory, cmd.Flags().Args()[len(args):])
		},
	}
	command.Flags().StringVar(&name, "name", "", "the mailbox name for this chat, e.g. worker-api")
	return command
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func open(cmd *cobra.Command, tool, name, directory string, toolArgs []string) error {
	if name == "" {
		return usage("this chat needs a name for the mailbox: mnemo run %s --name <name>", tool)
	}
	if !mailbox.IsName(name) {
		return usage("%q is not a usable name. Use lower-case letters, digits and dashes, starting with a "+
			"letter or a digit, up to 63 characters.", name)
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

	if directory == "" {
		directory, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return err
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a directory mnemo can open", directory)
	}

	// Registered before the tool starts. A chat that opens without mnemo has no
	// memory at all, and the failure is silent from inside it.
	if !registeredFor(tool, directory) {
		return fmt.Errorf("mnemo is not registered in %s. Run: mnemo mcp add %s", tool, tool)
	}
	executable, err := exec.LookPath(integration.Executable(tool))
	if err != nil {
		return fmt.Errorf("%s is not on the PATH, so mnemo cannot open it", integration.Executable(tool))
	}

	// The address has to be free. The claim that counts is the one the chat's own
	// `mnemo serve` makes, so this is a courtesy check that stops the usual
	// collision before a window opens rather than after.
	box := mailboxFor(resolved)
	address := name + "@" + machine
	if holder, live, err := box.HeldBy(cmd.Context(), address); err != nil {
		return err
	} else if live {
		return fmt.Errorf("%s is already open in %s. Close that chat, or pick another name",
			address, heldSince(holder))
	}

	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Opening %s as %s in %s.\n", tool, address, directory); err != nil {
		return err
	}
	return launch(directory, executable, toolArgs, append(os.Environ(),
		"MNEMO_AGENT="+name, "MNEMO_TOOL="+tool))
}

// heldSince says who has the name, in the words a person needs to go and find it.
func heldSince(holder mailbox.Holder) string {
	where := "another chat"
	if holder.Via == "remote" {
		where = "a chat on another machine"
	}
	return fmt.Sprintf("%s since %s", where, holder.Since.Local().Format("15:04"))
}

// registeredFor reports whether mnemo is registered for a tool, globally or in
// this directory. A project's own file counts: that is how a repository shares an
// entry with everybody who clones it.
func registeredFor(tool, directory string) bool {
	switch tool {
	case "codex":
		if where, err := configFileFor(tool); err == nil && integration.RegisteredCodex(where) {
			return true
		}
		return integration.RegisteredCodex(filepath.Join(directory, ".codex", "config.toml"))
	case "opencode":
		if where, err := configFileFor(tool); err == nil && integration.RegisteredOpencode(where) {
			return true
		}
		return integration.RegisteredOpencode(filepath.Join(directory, "opencode.json"))
	default:
		if integration.RegisteredClaude(nil) {
			return true
		}
		return integration.RegisteredClaudeFile(filepath.Join(directory, ".mcp.json"))
	}
}

// launch hands the terminal to the tool.
//
// On Unix it replaces this process, so the terminal, the job control and every
// signal belong to the tool and mnemo is not sitting in the middle of them. On
// Windows there is no exec, so it runs as a child with the same streams and exits
// with the tool's own status.
func launch(directory, executable string, args, environment []string) error {
	if err := os.Chdir(directory); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		child := exec.Command(executable, args...)
		child.Dir = directory
		child.Env = environment
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				os.Exit(exit.ExitCode())
			}
			return err
		}
		return nil
	}
	return syscall.Exec(executable, append([]string{executable}, args...), environment)
}
