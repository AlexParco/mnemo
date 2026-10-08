package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/mailbox"
	"github.com/spf13/cobra"
)

// `mnemo watch` is how a chat that is sitting idle learns it has post.
//
// It is run by the plugin's monitor, where a line on stdout becomes a
// notification and a notification reaches an idle chat. That is the only way in
// that needs no tool call at all, which makes it the only way to reach a chat
// that is doing nothing — and the reason a mailbox is useful rather than a
// curiosity.
//
// It only ever looks. It never marks a message read, never claims the name and
// never sends: the chat holding the name reads everything itself. Announcing is
// not delivering.

// WatchInterval is how often it looks. Two seconds is fast enough to feel
// immediate and slow enough to cost nothing on an idle machine.
const WatchInterval = 2 * time.Second

// previewChars is how much of a body a notification carries. Enough to decide
// whether to stop what you are doing; not enough to be the message.
const previewChars = 120

func watchCommand() *cobra.Command {
	var tool string
	command := &cobra.Command{
		Use:   "watch",
		Short: "Announce waiting mailbox messages, one line each",
		Long: "Run by the Claude Code plugin's monitor, never by hand. Each line it prints becomes a " +
			"notification, and a notification reaches a chat that is idle at its prompt.\n\n" +
			"It only looks: it never marks a message read, never claims the name and never sends.",
		// Hidden, and a contract with the plugin: `watch --tool` has to keep
		// working across releases, because a plugin and a binary of different
		// versions can meet.
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := settings()
			if err != nil {
				return err
			}
			return watch(cmd.Context(), resolved, tool, cmd.OutOrStdout(), cmd.ErrOrStderr(), WatchInterval)
		},
	}
	command.Flags().StringVar(&tool, "tool", "", "the tool this chat runs in, for group messages")
	return command
}

// watch announces, until the context ends or stdout closes.
func watch(ctx context.Context, resolved config.Settings, tool string, out, errOut io.Writer, every time.Duration) error {
	// A chat opened without `mnemo run` has no name, so it has no address and
	// nothing to announce. Nothing is wrong with that: it has memory and no
	// mailbox, by design. Exit quietly rather than explaining it every two
	// seconds for the life of the session.
	if resolved.Agent == "" || resolved.Machine == "" {
		return nil
	}
	address := resolved.Agent + "@" + resolved.Machine
	box := mailboxFor(resolved)

	// The tool is accepted because the plugin passes it and the flag is part of
	// that contract. Locally it is not needed: a group message is matched against
	// the tool recorded on the address when the chat claimed its name, which is
	// the one that is actually true rather than the one a watcher was told.
	_ = tool

	announced := 0
	lastError := ""
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		top, waiting, err := box.Peek(address, announced)
		switch {
		case err != nil:
			// Reported once per distinct error, not every two seconds for as
			// long as it lasts, and the watch carries on: a tunnel that drops
			// for a minute must not end it.
			if message := err.Error(); message != lastError {
				fmt.Fprintln(errOut, "mnemo watch: "+message)
				lastError = message
			}
		default:
			lastError = ""
			for _, message := range waiting {
				if _, err := fmt.Fprintln(out, notice(message)); err != nil {
					// Whoever was reading has gone. There is nobody left to
					// announce to, so stop rather than spin.
					return nil
				}
			}
			if top > announced {
				announced = top
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// notice is one line, and one notification.
func notice(message mailbox.Message) string {
	reply := ""
	if message.ReplyTo != "" {
		reply = " in reply to " + message.ReplyTo
	}
	return fmt.Sprintf("[mnemo] message %s from %s%s: %q Read it with mailbox_read.",
		message.ID, message.From, reply, preview(message.Body))
}

// preview is the body on one line, short enough to read at a glance.
//
// Counted in code points rather than bytes: cutting a body mid-rune would put a
// broken character in front of a person, and a notification is the one place
// mnemo's output is read by eye rather than by a model.
func preview(body string) string {
	// Whitespace is collapsed first and what is left is made printable after, in
	// that order: a newline is whitespace and becomes a space, while an escape
	// sequence is not text at all — a notification line that can repaint the
	// window it appears in can show somebody something mnemo never said.
	flat := printable(strings.Join(strings.Fields(body), " "))
	runes := []rune(flat)
	if len(runes) <= previewChars {
		return flat
	}
	return string(runes[:previewChars-1]) + "…"
}
