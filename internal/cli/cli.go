// Package cli is mnemo's commands. It is the only package that reads the
// environment, writes to a terminal and decides what the process does; every
// other package is given what it needs.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/lock"
	"github.com/AlexParco/mnemo/internal/mcpserver"
	"github.com/AlexParco/mnemo/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// Version is the binary's version, set at build time with -ldflags. A build
// from a working copy says so rather than claiming a release it is not.
var Version = "dev"

// flags are the overrides every command accepts, because a person debugging
// where their memory went needs to be able to point at another store without
// editing a file.
var flags config.Overrides

// usageError marks a failure that is the caller's typing rather than mnemo's
// doing, so a script can tell them apart.
type usageError struct{ error }

func usage(format string, args ...any) error { return usageError{fmt.Errorf(format, args...)} }

// exactly is cobra's ExactArgs, reporting a usage error rather than a plain one:
// the wrong number of arguments is the caller's typing, not mnemo failing.
func exactly(n int, shape string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) == n {
			return nil
		}
		return usage("this takes %s, and %d were given", shape, len(args))
	}
}

// noArgs is cobra's NoArgs, as a usage error.
func noArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return usage("this takes no arguments, and %d were given", len(args))
}

// Execute runs mnemo and returns the process's exit code.
func Execute(ctx context.Context) int {
	root := &cobra.Command{
		Use:   "mnemo",
		Short: "Shared memory and a mailbox for coding agents",
		Long: "mnemo keeps a person's project memory as plain text in a git store of their own, shared across " +
			"their machines, and serves it to coding agents over MCP.\n\n" +
			"Getting started\n" +
			"  mnemo mcp add                          register mnemo in the tools on this machine\n" +
			"  mnemo mcp status                       what is registered, and where the store is\n" +
			"  mnemo config                           where your memory is, and why\n" +
			"  mnemo config set store.remote <url>    a git hub to share it between machines\n" +
			"then open a chat in that tool and ask it to save this project's context.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&flags.Store, "store", "", "use this store instead of the configured one")
	root.PersistentFlags().StringVar(&flags.Machine, "machine", "", "label for this machine")
	root.PersistentFlags().StringVar(&flags.Lang, "lang", "", "language mnemo writes in: en or es")

	root.AddCommand(configCommand(), mcpCommand(), serveCommand(), versionCommand())

	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "mnemo:", err)
		var wrong usageError
		if errors.As(err, &wrong) {
			return 2
		}
		return 1
	}
	return 0
}

// settings reads the config file and resolves everything once, at the start of
// a command. A setting that did not apply is said out loud on stderr: a value a
// person set and mnemo quietly dropped is worse than a refusal.
func settings() (config.Settings, error) {
	file, err := config.Load()
	if err != nil {
		return config.Settings{}, err
	}
	resolved := config.Resolve(file, flags)
	for _, ignored := range resolved.Ignored {
		fmt.Fprintln(os.Stderr, "mnemo: "+ignored)
	}
	for _, unknown := range file.Unknown() {
		fmt.Fprintf(os.Stderr, "mnemo: %s does not know the setting %q in %s\n", Version, unknown, file.Path())
	}
	return resolved, nil
}

// memoryStore builds the store the write tools act through.
//
// It is built here and not inside mcpserver because building one means choosing
// where the lock file lives, and that is a fact about this machine. A server
// answering for other machines makes the same choice for itself.
func memoryStore(resolved config.Settings) *store.Store {
	return store.New(resolved.Paths.Store, store.Options{
		Locker: lock.New(resolved.Paths.Locks),
		Remote: resolved.Remote,
	})
}

func serveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Answer MCP tool calls on stdin and stdout",
		Long: "Started by the tool that opened the chat, never by hand. stdout carries the protocol, so nothing " +
			"else may be written there; anything mnemo has to say goes to stderr.",
		// Hidden, and a contract with the plugin: the interface of this command
		// has to keep working across releases, because a plugin and a binary of
		// different versions can meet.
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := settings()
			if err != nil {
				return err
			}
			// Refuse rather than serve against nowhere. Serving would answer
			// every call successfully while writing into the directory the
			// agent was started in.
			if _, err := resolved.RequireStore(); err != nil {
				return err
			}
			// The store is not created here. A chat that only reads gets an
			// answer saying there is nothing saved yet, which is true, and the
			// first write is what brings a store into being.
			return mcpserver.Serve(cmd.Context(), Version,
				mcpserver.Local(resolved, memoryStore(resolved)), &mcp.StdioTransport{})
		},
	}
}

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print mnemo's version",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), Version)
			return err
		},
	}
}
