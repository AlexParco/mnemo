package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/AlexParco/mnemo/internal/store"
	"github.com/spf13/cobra"
)

// `mnemo store remove` retires a store: the one destructive operation no agent
// can reach.
//
// Forgetting a project is behind a confirmation an agent shows a person.
// Removing everything is a different category — it has no undo — so it is a
// command a person types, and what it asks for is the store's own path. A yes is
// too easy to give by accident for something that cannot be taken back.

func storeCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "store",
		Short: "The memory store on this machine",
	}
	command.AddCommand(storeRemoveCommand())
	return command
}

func storeRemoveCommand() *cobra.Command {
	var purge, force bool
	command := &cobra.Command{
		Use:   "remove",
		Short: "Retire this machine's store: show what would go, then move it aside",
		Long: "Everything that would go is printed first — the path, every project with how many memories it " +
			"holds, and whether a hub has a copy — and then it asks for the store's path to be typed back.\n\n" +
			"By default the store is moved aside, next to itself, and the command says where it went and the " +
			"one line that deletes it for good. --purge deletes it instead. The hub is never touched: " +
			"whatever was pushed to it is still there, and that is the copy that outlives this machine.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := settings()
			if err != nil {
				return err
			}
			if _, err := resolved.RequireStore(); err != nil {
				return err
			}
			writable := memoryStore(resolved)
			plan, err := writable.PlanRetire(cmd.Context())
			if err != nil {
				return err
			}
			// A store that is not there is refused by the plan itself, with the
			// path in the message, so there is nothing to check for here.
			out := cmd.OutOrStdout()
			if err := showPlan(out, plan, purge); err != nil {
				return err
			}

			// The path, typed. Read from stdin rather than asked for with a
			// flag: a flag can sit in a shell history and be run again by
			// somebody who has forgotten what it does.
			typed, err := line(cmd.InOrStdin())
			if err != nil || typed != plan.Store {
				_, err := fmt.Fprintln(out, "  that is not the store's path. Nothing was done.")
				return err
			}

			result, err := writable.ApplyRetire(cmd.Context(), plan.Confirm,
				store.RetireOptions{Purge: purge, Force: force})
			if err != nil {
				return err
			}
			return showResult(out, result)
		},
	}
	command.Flags().BoolVar(&purge, "purge", false, "delete the store outright instead of moving it aside")
	command.Flags().BoolVar(&force, "force", false, "go ahead even though work has not reached the hub")
	return command
}

// showPlan is what the person reads before they decide. Projects are named, with
// their own counts: somebody recognises what they are about to lose by name, and
// a total alone is a number they cannot check.
func showPlan(out io.Writer, plan store.RetirePlan, purge bool) error {
	lines := []string{"store: " + plan.Store}
	if len(plan.Projects) == 0 {
		lines = append(lines, "projects: none")
	} else {
		lines = append(lines, fmt.Sprintf("projects: %d", len(plan.Projects)))
		rows := make([][]string, 0, len(plan.Projects))
		for _, project := range plan.Projects {
			rows = append(rows, []string{"  " + project.Slug, memoryCount(project.Memories)})
		}
		lines = append(lines, columns(rows)...)
	}
	lines = append(lines,
		fmt.Sprintf("memories: %d", plan.Memories),
		fmt.Sprintf("files in shared: %d", plan.SharedFiles))

	switch {
	case plan.Remote == "":
		lines = append(lines, "hub: none — these memories exist only here")
	case !plan.UnpushedKnown:
		lines = append(lines, "hub: "+plan.Remote+" — mnemo could not tell how much has reached it")
	case plan.Unpushed > 0:
		lines = append(lines, fmt.Sprintf("hub: %s — %d commit(s) have not reached it", plan.Remote, plan.Unpushed))
	default:
		lines = append(lines, "hub: "+plan.Remote+" — everything committed has reached it")
	}
	if plan.Dirty {
		lines = append(lines, "uncommitted changes: yes — they exist nowhere else")
	}

	lines = append(lines, "")
	if purge {
		lines = append(lines, "this deletes the store outright, and there is no undo")
	} else {
		lines = append(lines, "the store is moved aside, next to itself, not deleted")
	}
	// Said every time, including when there is no hub: what somebody needs to
	// know is whether a copy survives this, and the answer is the hub or nothing.
	if plan.Remote == "" {
		lines = append(lines, "nothing else has a copy of this")
	} else {
		lines = append(lines, "the hub is not touched: what was pushed to it stays there, and removing that is "+
			"done on the hub by whoever owns it")
	}
	lines = append(lines, "", "type the store's path to go ahead, or anything else to stop:")
	return say(out, lines)
}

func memoryCount(n int) string {
	if n == 1 {
		return "1 memory"
	}
	return fmt.Sprintf("%d memories", n)
}

func showResult(out io.Writer, result store.RetireResult) error {
	var lines []string
	if result.Purged {
		lines = append(lines, "deleted "+result.Store)
	} else {
		lines = append(lines,
			"moved "+result.Store+" to "+result.MovedTo,
			"delete it for good with: rm -rf "+shellQuote(result.MovedTo))
	}
	if result.Remote != "" {
		lines = append(lines, "the hub still has what was pushed to it: "+result.Remote)
	}
	// The config is left as it was, so the next save starts a fresh store and
	// adopts the hub's copy if there is one. Said because the alternative
	// reading — that mnemo is now unconfigured — would send somebody to set it
	// up again.
	lines = append(lines, "the config is unchanged, so the next save starts a new store here")
	return say(out, lines)
}

// shellQuote wraps a path for the one command the output tells somebody to run.
// A path with a space in it would otherwise be two arguments.
func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// line reads one line of input, without its ending.
func line(in io.Reader) (string, error) {
	var out []byte
	buffer := make([]byte, 1)
	for {
		n, err := in.Read(buffer)
		if n > 0 {
			if buffer[0] == '\n' {
				break
			}
			out = append(out, buffer[0])
		}
		if err != nil {
			if len(out) > 0 {
				break
			}
			return "", err
		}
	}
	return strings.TrimSpace(string(out)), nil
}
