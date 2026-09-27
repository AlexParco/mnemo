// Package cli is mnemo's commands. It is the only package that reads the
// environment, writes to a terminal and decides what the process does; every
// other package is given what it needs.
package cli

import (
	"context"
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

// Execute runs mnemo and returns the process's exit code.
func Execute(ctx context.Context) int {
	root := &cobra.Command{
		Use:   "mnemo",
		Short: "Shared memory and a mailbox for coding agents",
		Long: "mnemo keeps a person's project memory as plain text in a git store, shared across their machines, " +
			"and lets the agents working for them leave each other messages.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&flags.Store, "store", "", "use this store instead of the configured one")
	root.PersistentFlags().StringVar(&flags.Machine, "machine", "", "label for this machine")
	root.PersistentFlags().StringVar(&flags.Lang, "lang", "", "language for the card: en or es")

	root.AddCommand(configCommand(), mcpCommand(), serveCommand(), versionCommand())

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "mnemo:", err)
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
		Args:   cobra.NoArgs,
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
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), Version)
			return err
		},
	}
}
