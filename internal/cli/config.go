package cli

import (
	"fmt"
	"io"
	"os"
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
		Long: "Values come from a flag first, then the environment, then the config file, then the default, and " +
			"every line says which. The settings that can be set are: " + strings.Join(config.Settable(), ", ") +
			". A token is shown as `set`, never as itself.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, err := config.Load()
			if err != nil {
				return err
			}
			return showConfig(cmd.OutOrStdout(), file, config.Resolve(file, flags))
		},
	}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			_ = cmd.Help()
			return usage("config has no subcommand %q. It has: set, unset", args[0])
		}
		file, err := config.Load()
		if err != nil {
			return err
		}
		return showConfig(cmd.OutOrStdout(), file, config.Resolve(file, flags))
	}
	command.Args = cobra.ArbitraryArgs
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
	// Exactly one mode line, always: its absence used to be the only way to tell
	// a local machine from a connected one.
	mode := "local — this machine's own files"
	for _, remote := range file.Remotes {
		if remote.Connected {
			mode = fmt.Sprintf("connected to %s via %s", remote.Machine, remote.SSH)
		} else {
			mode = fmt.Sprintf("local — disconnected from %s (%s); mnemo connect reopens it",
				remote.Machine, remote.SSH)
		}
	}
	rows = append(rows, [3]string{"mode", mode, ""})

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
		Args:  exactly(2, "a key and a value"),
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
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Set %s in %s.\n", args[0], file.Path()); err != nil {
				return err
			}
			if note := afterSetting(args[0], args[1], config.Resolve(file, flags)); note != "" {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "\n"+note); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(),
				"\nA chat that is already open keeps the old value until it is restarted.")
			return err
		},
	}
}

// afterSetting is what the user needs to know next, for the settings where
// writing the value is not the whole job.
func afterSetting(key, value string, resolved config.Settings) string {
	switch key {
	case "store.remote":
		note := fmt.Sprintf("Nothing has been pushed or pulled. %s must already exist, and be either empty or a "+
			"mnemo store from another machine.", value)
		if _, err := os.Stat(resolved.Paths.Store); err != nil {
			return note + fmt.Sprintf("\n\nThere is no store on this machine yet (%s). It is created, and "+
				"pointed at that hub, by the first save from a chat. Pushing is the mnemo_push tool, or turn it "+
				"on for good with:\n  mnemo config set store.autopush true", resolved.Paths.Store)
		}
		return note + fmt.Sprintf("\n\nThe store at %s already exists; the next save from a chat repoints it "+
			"at the new hub.", resolved.Paths.Store)
	case "store.dir":
		return "Nothing was moved. This is where mnemo will look from now on; a store already at the old path " +
			"stays there."
	default:
		if name := overriding(key); name != "" {
			return fmt.Sprintf("warning: %s is set in the environment, and it wins over the file. Until you "+
				"unset it, this value has no effect.", name)
		}
		return ""
	}
}

// overriding names the variable that would beat a setting just written.
func overriding(key string) string {
	name := map[string]string{
		"machine": "MNEMO_MACHINE", "lang": "MNEMO_LANG", "store.dir": "MNEMO_DIR",
		"store.remote": "MNEMO_REMOTE", "store.autopush": "MNEMO_AUTOPUSH", "mailbox.dir": "MNEMO_MAILBOX_DIR",
	}[key]
	if name != "" && strings.TrimSpace(os.Getenv(name)) != "" {
		return name
	}
	return ""
}

func configUnsetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove one setting from the config file, so its default applies",
		Args:  exactly(1, "one key"),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := config.Load()
			if err != nil {
				return err
			}
			had := file.Has(args[0])
			if err := file.Unset(args[0]); err != nil {
				return err
			}
			if err := file.Save(); err != nil {
				return err
			}
			if !had {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s was not set in %s, so nothing changed; its default "+
					"already applies.\n", args[0], file.Path())
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed %s from %s; its default applies again.\n",
				args[0], file.Path())
			return err
		},
	}
}
