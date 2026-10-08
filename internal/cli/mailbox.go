package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/mailbox"
	"github.com/spf13/cobra"
)

// The mailbox from a terminal: peers, send, reply, read.
//
// They exist so a person can take part in the conversation their agents are
// having — hand out a task, read an answer — without opening a chat to do it.
// They are not how an agent uses the mailbox: an agent has the tools, which
// carry the rules with them, and a shell command does not.
//
// Everything except peers holds the name for as long as the command runs, which
// is what keeps one address to one live instance. peers only looks: it neither
// sends nor marks anything read, and being refused a look because a chat is open
// would be a refusal with nothing behind it.

// recent is how long after being seen an address is still "just now" rather than
// a time. A terminal command is quick, so without this the caller's own row
// would carry a clock reading of a second ago.
const recent = 90 * time.Second

func peersCommand() *cobra.Command {
	var name string
	command := &cobra.Command{
		Use:   "peers",
		Short: "Where the mailbox lives, and who is there",
		Long: "Every address the mailbox knows: the tool it runs in, whether a live instance holds it right " +
			"now, how many messages wait for it and when it was last heard from.\n\n" +
			"It always says which mailbox you are looking at. With --name it also records you as known, so " +
			"other agents can find your address.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// No claim: looking is not holding. A name given here is only so the
			// caller appears in the list and can be written to.
			_, box, caller, err := terminal(name, false)
			if err != nil {
				return err
			}
			peers, err := box.List(cmd.Context(), caller)
			if err != nil {
				return asCommand(err)
			}
			return printPeers(cmd.OutOrStdout(), peers, time.Now())
		},
	}
	command.Flags().StringVar(&name, "name", "", "the name to appear as, e.g. alex")
	return command
}

func sendCommand() *cobra.Command {
	var name, project string
	command := &cobra.Command{
		Use:   "send <to> <message>",
		Short: "Send a message to another agent",
		Long: "`to` is an address (name@machine), a bare name when only one agent has it, @all, or a tool " +
			"group such as @codex. The message waits if that agent is not open.\n\n" +
			"With --project the receiver is pointed at that project's memory instead of being sent the " +
			"context in the body.",
		Args: exactly(2, "an address and a message"),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, box, caller, err := terminal(name, true)
			if err != nil {
				return err
			}
			return holding(cmd.Context(), box, caller, func() error {
				sent, err := box.Send(cmd.Context(), caller, args[0], args[1], project)
				if err != nil {
					return asCommand(err)
				}
				return printSent(cmd.OutOrStdout(), caller, sent,
					fmt.Sprintf("sent %s to %s", sent.Message.ID, sent.Message.To))
			})
		},
	}
	command.Flags().StringVar(&name, "name", "", "the name to send as, e.g. alex")
	command.Flags().StringVar(&project, "project", "", "project slug the receiver should load first")
	return command
}

func replyCommand() *cobra.Command {
	var name string
	command := &cobra.Command{
		Use:   "reply <id> <message>",
		Short: "Answer a message, bound to its id",
		Long: "The answer goes back to whoever sent that message, carrying its id, so it cannot attach to " +
			"the wrong one. A message can be answered more than once.",
		Args: exactly(2, "a message id and a message"),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, box, caller, err := terminal(name, true)
			if err != nil {
				return err
			}
			return holding(cmd.Context(), box, caller, func() error {
				sent, err := box.Reply(cmd.Context(), caller, args[0], args[1])
				if err != nil {
					return asCommand(err)
				}
				return printSent(cmd.OutOrStdout(), caller, sent, fmt.Sprintf("sent %s in reply to %s to %s",
					sent.Message.ID, sent.Message.ReplyTo, sent.Message.To))
			})
		},
	}
	command.Flags().StringVar(&name, "name", "", "the name to answer as, e.g. alex")
	return command
}

func readCommand() *cobra.Command {
	var name string
	command := &cobra.Command{
		Use:   "read",
		Short: "Read the messages waiting for a name, and mark them read",
		Long: "What is waiting for this name, oldest first, and then it is read: a second call shows nothing " +
			"of the same messages. The cursor belongs to the address, so it is the same cursor a chat opened " +
			"under that name would use.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, box, caller, err := terminal(name, true)
			if err != nil {
				return err
			}
			return holding(cmd.Context(), box, caller, func() error {
				messages, err := box.Read(cmd.Context(), caller)
				if err != nil {
					return asCommand(err)
				}
				return printMessages(cmd.OutOrStdout(), caller, messages, time.Now())
			})
		},
	}
	command.Flags().StringVar(&name, "name", "", "the name to read for, e.g. alex")
	return command
}

// asCommand turns the tool names in a refusal into the commands a person can
// actually type.
//
// The mailbox writes one message for both surfaces, which is right — two
// wordings of the same refusal would drift — but the names in it belong to the
// MCP tools. Telling somebody in a shell to "check the spelling with
// mailbox_peers" is telling them to do something they cannot do.
func asCommand(err error) error {
	if err == nil {
		return nil
	}
	said := commandsFor(err.Error())
	if said == err.Error() {
		return err
	}
	// Kept as a refusal rather than rewritten into a plain error: what it is
	// decides the exit status.
	if mailbox.IsRefusal(err) {
		return &mailbox.Refusal{Message: said}
	}
	return errors.New(said)
}

// commandsFor is the translation itself, on a plain string.
func commandsFor(said string) string {
	commands := map[string]string{
		"mailbox_peers": "mnemo peers",
		"mailbox_send":  "mnemo send",
		"mailbox_reply": "mnemo reply",
		"mailbox_read":  "mnemo read",
	}
	// Listing projects is the one thing a person has no command for, so they
	// are pointed at the directory rather than at a tool they cannot call. The
	// settings are read here, in a path that is already a failure, so that no
	// command has to carry the store around for the sake of one sentence.
	if strings.Contains(said, "mnemo_list_projects") {
		if file, err := config.Load(); err == nil {
			if store := config.Resolve(file, flags).Paths.Store; store != "" {
				commands["mnemo_list_projects"] = "ls " + filepath.Join(store, "projects")
			}
		}
	}
	for tool, command := range commands {
		said = strings.ReplaceAll(said, tool, command)
	}
	return said
}

// terminal works out who the person is: the name they gave, else MNEMO_AGENT,
// and this machine's label.
//
// A command that needs a name and has none stops. Guessing one would mean
// sending under a name whose answers go somewhere this person is not looking.
func terminal(name string, needed bool) (config.Settings, *mailbox.Mailbox, mailbox.Caller, error) {
	var caller mailbox.Caller
	resolved, err := settings()
	if err != nil {
		return resolved, nil, caller, err
	}
	machine, err := resolved.RequireMachine()
	if err != nil {
		return resolved, nil, caller, err
	}
	if name == "" {
		name = resolved.Agent
	}
	if name == "" && needed {
		return resolved, nil, caller, usage(
			"this needs a name to act as: --name <name>, or MNEMO_AGENT in the environment")
	}
	if name != "" && !mailbox.IsName(name) {
		return resolved, nil, caller, usage("%q is not a usable name. Use lower-case letters, digits and "+
			"dashes, starting with a letter or a digit, up to 63 characters.", name)
	}
	return resolved, mailboxFor(resolved), mailbox.Caller{
		Name: name, Machine: machine,
		// The group a terminal belongs to, so @terminal reaches it.
		Tool: "terminal", Instance: mailbox.Instance(), Via: "local", PID: os.Getpid(),
	}, nil
}

// holding runs a command with the name claimed, and gives it back afterwards.
//
// The claim is what makes a terminal command and a chat the same kind of thing:
// both hold one address, and a command run while a chat holds that name is
// refused rather than quietly reading the chat's post out from under it.
func holding(ctx context.Context, box *mailbox.Mailbox, caller mailbox.Caller, fn func() error) error {
	if _, err := box.Claim(ctx, caller); err != nil {
		return asCommand(err)
	}
	// Released on the way out, including on a failure: a command that leaves the
	// name held would keep a chat from opening under it until the lease or the
	// process check noticed, and there is no process to notice here.
	defer func() { _ = box.Release(context.Background(), caller) }()
	return fn()
}

func printPeers(out io.Writer, peers mailbox.Peers, now time.Time) error {
	lines := []string{"mailbox on " + peers.Machine + " (local mode)"}
	if peers.Migrated != "" {
		lines = append(lines, "an earlier install's messages were moved to "+peers.Migrated+
			"; the mailbox started empty")
	}
	if len(peers.Peers) == 0 {
		lines = append(lines, "no agents known yet — open one with: mnemo run <tool> --name <name>")
	} else {
		header := []string{"ADDRESS", "TOOL", "STATUS", "UNREAD", "LAST SEEN"}
		rows := [][]string{header}
		for _, peer := range peers.Peers {
			row := []string{peer.Address, orUnknown(peer.Tool), status(peer.Live),
				fmt.Sprint(peer.Unread), seen(peer.LastSeen, now)}
			if peer.Caller {
				row = append(row, "(you)")
			}
			rows = append(rows, row)
		}
		lines = append(lines, columns(rows)...)
	}
	return say(out, lines)
}

// columns lines a table up. Every field but the last on its row is padded to
// the width of its column, so the table reads down as well as across; a row may
// carry one field more than the header, which is how `(you)` is marked.
func columns(rows [][]string) []string {
	var widths []int
	for _, row := range rows {
		for i, field := range row {
			for len(widths) <= i {
				widths = append(widths, 0)
			}
			if width := len([]rune(field)); width > widths[i] {
				widths[i] = width
			}
		}
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		var line strings.Builder
		for i, field := range row {
			if i > 0 {
				line.WriteString("   ")
			}
			if i == len(row)-1 {
				line.WriteString(field)
				continue
			}
			line.WriteString(pad(field, widths[i]))
		}
		out = append(out, line.String())
	}
	return out
}

func pad(field string, width int) string {
	if width -= len([]rune(field)); width <= 0 {
		return field
	}
	return field + strings.Repeat(" ", width)
}

func status(live bool) string {
	if live {
		return "live"
	}
	return "offline"
}

func orUnknown(tool string) string {
	if tool == "" {
		return "unknown"
	}
	return tool
}

// seen is when an address was last heard from, as a person reads it: the clock
// for today, the date for an earlier day, and words for a moment ago.
func seen(at, now time.Time) string {
	switch {
	case at.IsZero():
		return "never"
	case now.Sub(at) < recent && now.Sub(at) >= 0:
		return "just now"
	case sameDay(at.Local(), now.Local()):
		return at.Local().Format("15:04")
	default:
		return at.Local().Format("2006-01-02 15:04")
	}
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func printSent(out io.Writer, caller mailbox.Caller, sent mailbox.Sent, first string) error {
	lines := []string{first + " as " + caller.Address()}
	if sent.Warning != "" {
		lines = append(lines, "warning: "+warnAsCommand(sent.Warning))
	}
	return say(out, lines)
}

// warnAsCommand is asCommand for a warning, which is a string rather than an
// error because nothing failed.
func warnAsCommand(warning string) string {
	return commandsFor(warning)
}

func printMessages(out io.Writer, caller mailbox.Caller, messages []mailbox.Message, now time.Time) error {
	if len(messages) == 0 {
		return say(out, []string{"no new messages for " + caller.Address()})
	}
	var lines []string
	for _, message := range messages {
		parts := []string{message.ID, "from " + message.From + " (" + orUnknown(message.Tool) + ")",
			when(message.TS, now)}
		if message.ReplyTo != "" {
			parts = append(parts, "in reply to "+message.ReplyTo)
		}
		if message.Project != "" {
			parts = append(parts, "project "+message.Project)
		}
		lines = append(lines, strings.Join(parts, " · "))
		// Indented under its header, so a body of several lines is still clearly
		// one message rather than running into the next.
		for _, line := range strings.Split(message.Body, "\n") {
			lines = append(lines, "  "+line)
		}
	}
	return say(out, lines)
}

// when is a message's stamp as a person reads it. A stamp that cannot be parsed
// is printed as it is rather than dropped: it came from the file, and showing it
// is how somebody finds out why it looks wrong.
func when(stamp string, now time.Time) string {
	at, err := time.Parse("2006-01-02T15:04:05.000Z", stamp)
	if err != nil {
		return stamp
	}
	if sameDay(at.Local(), now.Local()) {
		return at.Local().Format("15:04")
	}
	return at.Local().Format("2006-01-02 15:04")
}

// say prints the lines a terminal command answers with, each indented by two
// spaces, which is how every other command here answers.
func say(out io.Writer, lines []string) error {
	for _, line := range lines {
		if _, err := fmt.Fprintln(out, "  "+line); err != nil {
			return err
		}
	}
	return nil
}
