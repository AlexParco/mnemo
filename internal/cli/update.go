package cli

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/AlexParco/mnemo/internal/build"
	"github.com/AlexParco/mnemo/internal/integration"
	"github.com/AlexParco/mnemo/internal/selfupdate"
	"github.com/AlexParco/mnemo/internal/service"
	"github.com/AlexParco/mnemo/internal/tunnel"
	"github.com/spf13/cobra"
)

// `mnemo update` replaces this binary with another release.
//
// What it restarts afterwards is the point of wiring it here rather than in the
// package that does the work: whether this machine has a service, whether a
// tunnel keeper is running and whether the plugin is installed are three
// questions about this machine, and the package that replaces a binary should
// not have to know how to answer any of them.

func updateCommand() *cobra.Command {
	var wanted string
	command := &cobra.Command{
		Use:   "update",
		Short: "Download, verify and replace this binary",
		Long: "The archive is verified against the release's checksums before anything on disk changes, so a " +
			"download that does not match leaves this machine exactly as it was.\n\n" +
			"Afterwards the server service and the tunnel keeper are restarted if this machine has them, " +
			"and the Claude Code plugin is updated if it is installed. Open chats use the new version " +
			"after they restart.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := settings()
			if err != nil {
				return err
			}

			options := selfupdate.Options{Running: build.Version, Wanted: wanted}

			// Only what is really here is restarted. A nil means there is
			// nothing of that kind on this machine, which is different from a
			// restart that failed — and the difference is what the person reads
			// at the end.
			manager := service.New(resolved, mnemoPath())
			if manager.State().Installed {
				options.RestartService = func(context.Context) error {
					result, err := manager.Restart()
					if err != nil {
						return err
					}
					if result.Outcome == service.Refused {
						return fmt.Errorf("%s", result.Detail)
					}
					return nil
				}
			}
			keeper := tunnel.New(resolved.Paths, tunnel.Options{})
			if _, running := keeper.Running(); running {
				options.RestartTunnel = func(ctx context.Context) error {
					// Stopped and started rather than signalled: the keeper that
					// is running is the old binary, and the point is to be
					// running the new one.
					if err := keeper.Stop(ctx); err != nil {
						return err
					}
					return keeper.Start()
				}
			}
			if integration.Installed("claude") && integration.RegisteredClaude(nil) {
				options.UpdatePlugin = func(ctx context.Context) error {
					out, err := exec.CommandContext(ctx, "claude", "plugin", "update",
						integration.PluginRef).CombinedOutput()
					if err != nil {
						return fmt.Errorf("%s: %w", firstLine(string(out)), err)
					}
					return nil
				}
			}

			result, err := selfupdate.Update(cmd.Context(), options)
			if err != nil {
				if selfupdate.IsRefusal(err) {
					// Something to read, not something that failed: the person
					// is under a package manager, or a checksum did not match.
					return say(cmd.OutOrStdout(), []string{err.Error()})
				}
				return err
			}

			lines := []string{result.Line()}
			lines = append(lines, result.Trouble...)
			return say(cmd.OutOrStdout(), lines)
		},
	}
	command.Flags().StringVar(&wanted, "version", "",
		"the release to install, like v0.6.0; the latest by default")
	return command
}

// firstLine is the useful part of a tool's output in a sentence: the rest is
// usually a usage message nobody asked for.
func firstLine(said string) string {
	for i, letter := range said {
		if letter == '\n' {
			return said[:i]
		}
	}
	return said
}
