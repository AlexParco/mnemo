package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/spf13/cobra"
)

// `mnemo config` answers one question: why is my memory where it is. So every
// line says not only the value but where the value came from — a flag, a
// variable, the file, or nothing at all.

func configCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Show every setting, its value and where it came from",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, err := config.Load()
			if err != nil {
				return err
			}
			return showConfig(cmd.OutOrStdout(), file, config.Resolve(file, flags))
		},
	}
	command.AddCommand(configSetCommand(), configUnsetCommand())
	return command
}

// origin puts a Source into the words a person reads. The struct is data; this
// is how it is said out loud.
func origin(source config.Source) string {
	switch {
	case source.Kind == "default" && source.Where != "":
		return source.Where
	case source.Kind == "default":
		return "default"
	case source.Kind == "config":
		return "config file"
	case source.Kind == "env":
		return "environment (" + source.Where + ")"
	default:
		return source.Kind + " (" + source.Where + ")"
	}
}

func showConfig(out io.Writer, file *config.File, resolved config.Settings) error {
	rows := [][3]string{
		{"machine", resolved.Machine, origin(resolved.Source("machine"))},
		{"lang", resolved.Lang, origin(resolved.Source("lang"))},
		{"store.dir", resolved.Paths.Store, origin(resolved.Source("store"))},
		{"store.remote", resolved.Remote, origin(resolved.Source("remote"))},
		{"store.autopush", fmt.Sprintf("%t", resolved.Autopush), origin(resolved.Source("autopush"))},
		{"mailbox.dir", resolved.Paths.Mailbox, origin(resolved.Source("mailbox"))},
	}
	// The token is never printed. A config listing ends up in terminals,
	// transcripts and screenshots.
	if file.Server.Token != "" || file.Server.Port != 0 {
		rows = append(rows, [3]string{"server.port", fmt.Sprintf("%d", file.Server.Port), "config file"})
		rows = append(rows, [3]string{"server.token", config.Mask(file.Server.Token), "config file"})
	}
	for _, remote := range file.Remotes {
		state := "disconnected"
		if remote.Connected {
			state = "connected"
		}
		rows = append(rows, [3]string{"mode", fmt.Sprintf("%s to %s via %s", state, remote.Machine, remote.SSH), "config file"})
	}

	table := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	for _, row := range rows {
		value := row[1]
		if value == "" {
			value = "—"
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\n", row[0], value, row[2]); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(table, "config file\t%s\t\n", file.Path()); err != nil {
		return err
	}
	if err := table.Flush(); err != nil {
		return err
	}

	// A value the user set and mnemo could not use is said here too, not only on
	// stderr where a command's own output would bury it.
	for _, ignored := range resolved.Ignored {
		if _, err := fmt.Fprintln(out, "\nwarning: "+ignored); err != nil {
			return err
		}
	}
	if unknown := file.Unknown(); len(unknown) > 0 {
		sort.Strings(unknown)
		_, err := fmt.Fprintf(out, "\nKept, but not settings mnemo knows: %s\n", strings.Join(unknown, ", "))
		return err
	}
	return nil
}

func configSetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Write one setting to the config file",
		Long:  "Settings that can be set: " + strings.Join(config.Settable(), ", "),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := config.Load()
			if err != nil {
				return err
			}
			if err := file.Set(args[0], args[1]); err != nil {
				return err
			}
			if err := file.Save(); err != nil {
				return err
			}
			// Said every time, because the surprise otherwise arrives much
			// later: a chat already open kept the old value and nobody knows why.
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Set %s in %s.\nA chat that is already open keeps the old "+
				"value until it is restarted.\n", args[0], file.Path())
			return err
		},
	}
}

func configUnsetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove one setting from the config file, so its default applies",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := config.Load()
			if err != nil {
				return err
			}
			if err := file.Unset(args[0]); err != nil {
				return err
			}
			if err := file.Save(); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed %s from %s; its default applies again.\n",
				args[0], file.Path())
			return err
		},
	}
}
