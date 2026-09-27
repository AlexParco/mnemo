package cli

import (
	"fmt"
	"io"
	"slices"

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
	}
	command.AddCommand(mcpAddCommand(), mcpRemoveCommand())
	return command
}

// toolArg validates the optional tool name.
func toolArg(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if len(args) > 1 {
		return fmt.Errorf("name one tool, or none for all of them")
	}
	if !slices.Contains(integration.Names, args[0]) {
		return fmt.Errorf("%q is not a tool mnemo knows: %v", args[0], integration.Names)
	}
	return nil
}

func mcpAddCommand() *cobra.Command {
	var path string
	command := &cobra.Command{
		Use:   "add [claude|codex|opencode]",
		Short: "Add mnemo's entry, or install the Claude Code plugin",
		Long: "With no tool named, each of the three is added when its executable is on the PATH and skipped " +
			"otherwise. Running it again rewrites mnemo's own entry with the current path and changes nothing " +
			"else. An entry mnemo did not write is never touched.",
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
			results = append(results, integration.Result{Tool: tool, Outcome: integration.Skipped})
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
	switch tool {
	case "codex":
		return integration.AddCodex(orDefault(path, integration.CodexFile()), mnemo)
	case "opencode":
		return integration.AddOpencode(orDefault(path, integration.OpencodeFile()), mnemo)
	default:
		if path != "" {
			return integration.AddClaudeFile(path, mnemo)
		}
		return integration.AddClaude(nil)
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
	switch tool {
	case "codex":
		return integration.RemoveCodex(orDefault(path, integration.CodexFile()))
	case "opencode":
		return integration.RemoveOpencode(orDefault(path, integration.OpencodeFile()))
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

func orDefault(path, fallback string) string {
	if path != "" {
		return path
	}
	return fallback
}

// report prints one line per tool and fails only when something was refused, so
// a skipped tool does not look like an error to a script.
func report(out io.Writer, results []integration.Result) error {
	refused := 0
	for _, result := range results {
		if _, err := fmt.Fprintln(out, result.String()); err != nil {
			return err
		}
		if result.Outcome == integration.Refused {
			refused++
		}
	}
	if refused > 0 {
		return fmt.Errorf("%d tool(s) were not changed", refused)
	}
	return nil
}
