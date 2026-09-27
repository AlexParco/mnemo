package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/AlexParco/mnemo/internal/integration"
	"github.com/spf13/cobra"
)

// `mnemo mcp add` and `remove`. One line of output per tool, always, including
// the ones that were skipped: a silent skip is how somebody spends an evening
// wondering why their memory is not there.

func mcpCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Register mnemo in Claude Code, Codex and opencode",
		// Naming no subcommand is a usage error, not a successful no-op.
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Help()
			return usage("mcp needs a subcommand: add, remove or status")
		},
	}
	command.AddCommand(mcpAddCommand(), mcpRemoveCommand(), mcpStatusCommand())
	return command
}

// toolArg validates the optional tool name.
func toolArg(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if len(args) > 1 {
		return usage("name one tool, or none for all of them")
	}
	// --path writes one file, so it needs one tool: with none, the first tool
	// would write its format into the file and the next would refuse it.
	if path, _ := cmd.Flags().GetString("path"); path != "" && len(args) == 0 {
		return usage("--path writes one tool's file, so name the tool: mnemo mcp add codex --path %s", path)
	}
	if !slices.Contains(integration.Names, args[0]) {
		return usage("%q is not a tool mnemo knows. It knows: %s", args[0], strings.Join(integration.Names, ", "))
	}
	return nil
}

func mcpAddCommand() *cobra.Command {
	var path string
	command := &cobra.Command{
		Use:   "add [claude|codex|opencode]",
		Short: "Add mnemo's entry, or install the Claude Code plugin",
		Long: "With no tool named, each of the three is added when its executable is on the PATH and skipped " +
			"otherwise. With a tool named, it is registered even if that tool is not installed — except Claude " +
			"Code, whose plugin needs the claude command.\n\n" +
			"Running it again rewrites mnemo's own entry with the current path and changes nothing else. An entry " +
			"mnemo did not write is never touched: that is a refusal, it exits 1, and nothing is written.\n\n" +
			"--path takes one file and needs one tool named. For claude it writes an .mcp.json instead of " +
			"installing the plugin, which gives the MCP tools but not the mailbox monitor or the /mnemo:* commands.",
		Args: toolArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			mnemo, err := integration.MnemoPath()
			if err != nil {
				return err
			}
			return report(cmd.OutOrStdout(), addAll(args, path, mnemo))
		},
	}
	command.Flags().StringVar(&path, "path", "",
		"write the entry to this file instead, rather than the tool's own configuration")
	return command
}

func addAll(args []string, path, mnemo string) []integration.Result {
	var results []integration.Result
	for _, tool := range chosen(args) {
		named := len(args) == 1
		if !named && !integration.Installed(tool) {
			results = append(results, integration.Result{Tool: tool, Outcome: integration.Skipped,
				Detail: integration.Executable(tool) + " is not on the PATH"})
			continue
		}
		result, err := addOne(tool, path, mnemo)
		if err != nil {
			result = integration.Result{Tool: tool, Outcome: integration.Refused, Detail: err.Error()}
		}
		results = append(results, result)
	}
	return results
}

func addOne(tool, path, mnemo string) (integration.Result, error) {
	if path == "" {
		if where, err := configFileFor(tool); err != nil {
			return integration.Result{Tool: tool, Outcome: integration.Refused, Detail: err.Error(),
				Advice: fmt.Sprintf("Name the file yourself: mnemo mcp add %s --path /path/to/config", tool)}, nil
		} else {
			path = where
		}
	}
	switch tool {
	case "codex":
		return integration.AddCodex(path, mnemo)
	case "opencode":
		return integration.AddOpencode(path, mnemo)
	default:
		if path != "" {
			return integration.AddClaudeFile(path, mnemo)
		}
		return integration.AddClaude(Version, nil)
	}
}

func mcpRemoveCommand() *cobra.Command {
	var path string
	command := &cobra.Command{
		Use:   "remove [claude|codex|opencode]",
		Short: "Remove mnemo's entry, or uninstall the Claude Code plugin",
		Long: "Only what mnemo wrote is removed. An entry it did not write is reported and left alone. " +
			"Nothing to remove is not a failure.",
		Args: toolArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			var results []integration.Result
			for _, tool := range chosen(args) {
				result, err := removeOne(tool, path)
				if err != nil {
					result = integration.Result{Tool: tool, Outcome: integration.Refused, Detail: err.Error()}
				}
				results = append(results, result)
			}
			return report(cmd.OutOrStdout(), results)
		},
	}
	command.Flags().StringVar(&path, "path", "", "remove the entry from this file instead")
	return command
}

func removeOne(tool, path string) (integration.Result, error) {
	if path == "" && tool != "claude" {
		where, err := configFileFor(tool)
		if err != nil {
			return integration.Result{Tool: tool, Outcome: integration.Nothing, Detail: err.Error()}, nil
		}
		path = where
	}
	switch tool {
	case "codex":
		return integration.RemoveCodex(path)
	case "opencode":
		return integration.RemoveOpencode(path)
	default:
		if path != "" {
			return integration.RemoveClaudeFile(path)
		}
		return integration.RemoveClaude(nil)
	}
}

func chosen(args []string) []string {
	if len(args) == 1 {
		return args
	}
	return integration.Names
}

// configFileFor is where a tool keeps its configuration, or why mnemo cannot say.
//
// Without a home directory there is no answer. Joining an empty base gives a
// relative path, and the entry would land in whatever directory the command
// happened to be run from — inside somebody's repository, where the tool will
// never look for it.
func configFileFor(tool string) (string, error) {
	var path string
	switch tool {
	case "codex":
		path = integration.CodexFile()
	case "opencode":
		path = integration.OpencodeFile()
	default:
		return "", nil
	}
	if path == "" {
		return "", fmt.Errorf("this machine has no home directory, so mnemo cannot find where %s keeps its config",
			tool)
	}
	return path, nil
}

// report prints one line per tool and fails only when something was refused, so
// a skipped tool does not look like an error to a script.
func report(out io.Writer, results []integration.Result) error {
	var refusedTools []string
	skipped := 0
	for _, result := range results {
		if _, err := fmt.Fprintln(out, result.String()); err != nil {
			return err
		}
		switch result.Outcome {
		case integration.Refused:
			refusedTools = append(refusedTools, result.Tool)
		case integration.Skipped:
			skipped++
		}
	}
	if skipped == len(results) && len(results) > 0 {
		// Nothing happened and nothing is wrong. Said out loud, with the way
		// round it: a tool can be registered before it is installed.
		if _, err := fmt.Fprint(out, "\nNone of the three tools was found, so nothing was registered. Install "+
			"one and run this again, or name a tool to register it anyway:\n"+
			"    mnemo mcp add codex      writes Codex's config.toml\n"+
			"    mnemo mcp add opencode   writes opencode.json\n"); err != nil {
			return err
		}
	}
	if len(refusedTools) > 0 {
		return fmt.Errorf("%s not changed; nothing was written", strings.Join(refusedTools, ", "))
	}
	return nil
}

// `mnemo mcp status` is the answer to "did that work". Without it the only way to
// find out was to open a chat.
func mcpStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Say which tools mnemo is registered in, and where the store is",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			for _, tool := range integration.Names {
				line := fmt.Sprintf("%-10s ", tool)
				switch {
				case !integration.Installed(tool):
					line += "not installed"
				case registered(tool):
					line += "registered"
					if where, err := configFileFor(tool); err == nil && where != "" {
						line += "   " + where
					}
				default:
					line += "not registered"
					if tool == "claude" {
						line += "   the plugin " + integration.PluginRef + " is not installed"
					}
				}
				if _, err := fmt.Fprintln(out, line); err != nil {
					return err
				}
			}

			resolved, err := settings()
			if err != nil {
				return err
			}
			store := resolved.Paths.Store
			state := "exists"
			if store == "" {
				store, state = "nowhere", "this machine has no home directory"
			} else if _, err := os.Stat(store); err != nil {
				state = "not created yet — the first save from a chat creates it"
			}
			if _, err := fmt.Fprintf(out, "%-10s %s   %s\n", "store", store, state); err != nil {
				return err
			}
			hub := resolved.Remote
			if hub == "" {
				hub = "none — nothing you save leaves this machine"
			}
			_, err = fmt.Fprintf(out, "%-10s %s\n", "hub", hub)
			return err
		},
	}
}

func registered(tool string) bool {
	switch tool {
	case "codex":
		where, err := configFileFor(tool)
		return err == nil && integration.RegisteredCodex(where)
	case "opencode":
		where, err := configFileFor(tool)
		return err == nil && integration.RegisteredOpencode(where)
	default:
		return integration.RegisteredClaude(nil)
	}
}
