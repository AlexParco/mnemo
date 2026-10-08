package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AlexParco/mnemo/internal/config"
	"github.com/spf13/cobra"
)

// `mnemo hook suggest-save` nudges toward saving when unsaved work piles up.
//
// Everything about it is shaped by one rule: **it never gets in the way.** It
// runs before every edit, in somebody's working session, and a hook that fails
// loudly or blocks a write would be worse than no hook at all. So every error
// here ends silently with exit code 0, and the only thing it ever does is add a
// sentence.

// Defaults for the two signals. Edits are counted because counting them is
// reliable; context size is read from the transcript, whose format belongs to the
// tool and may change, which is why it is off unless somebody asks for it.
const (
	defaultSaveEdits  = 40
	defaultSaveTokens = 0
	defaultTokensStep = 60000
	stateLifetime     = 7 * 24 * time.Hour
)

// hookInput is the part of the hook's payload this command uses.
type hookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
}

// sessionState is what is remembered between calls, per session.
type sessionState struct {
	Edits             int    `json:"edits"`
	EditsAtLastNudge  int    `json:"editsAtLastNudge"`
	TokensAtLastNudge int    `json:"tokensAtLastNudge"`
	StoreHead         string `json:"storeHead"`
	// AnnouncedPost is the highest message seq this session has already been
	// told about, so a chat that is working hard is told once per message rather
	// than at every edit until it reads them.
	AnnouncedPost int `json:"announcedPost"`
}

func hookCommand() *cobra.Command {
	command := &cobra.Command{
		Use:    "hook",
		Short:  "Hooks the Claude Code plugin installs",
		Hidden: true,
	}
	command.AddCommand(&cobra.Command{
		Use:   "suggest-save",
		Short: "Nudge toward saving when unsaved work piles up",
		Long: "Reads the hook's JSON on stdin and prints a nudge on stdout, or nothing. It never blocks an " +
			"edit and never fails: every error ends quietly, because a broken hook must not get in the way of " +
			"somebody's work.",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The error is deliberately dropped. A hook that reports a problem
			// reports it into the middle of somebody's session.
			if message := suggestSave(cmd.InOrStdin()); message != "" {
				printNudge(cmd.OutOrStdout(), message)
			}
			return nil
		},
	})
	return command
}

// printNudge writes the shape the hook protocol expects: the same sentence twice,
// once for the person and once for the model.
func printNudge(out io.Writer, message string) {
	encoded, err := json.Marshal(map[string]any{
		"systemMessage": message,
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PreToolUse",
			"additionalContext": message,
		},
	})
	if err != nil {
		return
	}
	fmt.Fprintln(out, string(encoded))
}

// suggestSave decides whether to nudge, and with what. An empty string is the
// normal answer.
func suggestSave(stdin io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return ""
	}
	var input hookInput
	if err := json.Unmarshal(raw, &input); err != nil || input.SessionID == "" {
		// No session means no state to keep and nothing to count against.
		return ""
	}

	file, err := config.Load()
	if err != nil {
		return ""
	}
	resolved := config.Resolve(file, config.Overrides{})
	if resolved.Paths.Store == "" {
		return ""
	}

	edits := envInt("MNEMO_SAVE_EDITS", defaultSaveEdits)
	tokens := envInt("MNEMO_SAVE_TOKENS", defaultSaveTokens)
	step := envInt("MNEMO_SAVE_TOKENS_STEP", defaultTokensStep)

	dir := resolved.Paths.Reminder
	sweepOldState(dir, time.Now())
	path := filepath.Join(dir, input.SessionID+".json")
	state := loadSessionState(path)

	// A save resets the count. The store's head is read from the files in .git
	// rather than by running git: this is on the path of every edit, and starting
	// a process there would be felt.
	if head := storeHead(resolved.Paths.Store); head != "" && head != state.StoreHead {
		if state.StoreHead != "" {
			state.Edits, state.EditsAtLastNudge = 0, 0
		}
		state.StoreHead = head
	}
	state.Edits++

	message := ""
	spanish := resolved.Lang == "es"

	// The post comes first, and it is the reason this hook mentions anything at
	// all to a chat that is working hard: it is neither idle, so the watcher's
	// notification is not what wakes it, nor calling mnemo, so no answer carries
	// the line. Its next edit is.
	post := waitingPost(resolved, &state, spanish)

	saved := true
	if _, err := os.Stat(resolved.Paths.Store); err != nil {
		// Nothing has ever been saved here, so there is nothing to nudge about —
		// but there may still be post, which is why this is no longer the end.
		saved = false
	}

	// The edit signal: reliable, and the one that is on by default.
	if saved && edits > 0 && state.Edits-state.EditsAtLastNudge >= edits {
		message = nudge(spanish, editReason(spanish, state.Edits))
		state.EditsAtLastNudge = state.Edits
	}

	// The context signal: off unless asked for, because it reads a transcript
	// whose format belongs to the tool and can change under us.
	if saved && message == "" && tokens > 0 && input.TranscriptPath != "" {
		if used := contextTokens(input.TranscriptPath); used >= tokens && used >= state.TokensAtLastNudge+step {
			message = nudge(spanish, tokenReason(spanish, used))
			state.TokensAtLastNudge = used
		}
	}

	saveSessionState(path, state)
	return strings.TrimSpace(strings.Join(nonEmpty(post, message), " "))
}

// nonEmpty drops the parts there was nothing to say about.
func nonEmpty(parts ...string) []string {
	var out []string
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// waitingPost is the sentence about messages waiting for this chat, and the
// empty string when there are none or when this chat has no name.
//
// It peeks, which takes no lock and marks nothing read: a hook runs before
// somebody's edit, and it must never be able to block another agent or to
// consume the message it is announcing. The chat still reads with mailbox_read.
func waitingPost(resolved config.Settings, state *sessionState, spanish bool) string {
	if resolved.Agent == "" || resolved.Machine == "" {
		return ""
	}
	address := resolved.Agent + "@" + resolved.Machine
	top, waiting, err := mailboxFor(resolved).Peek(address, state.AnnouncedPost)
	if err != nil || len(waiting) == 0 {
		return ""
	}
	if top > state.AnnouncedPost {
		state.AnnouncedPost = top
	}
	senders := map[string]bool{}
	var order []string
	for _, message := range waiting {
		if !senders[message.From] {
			senders[message.From] = true
			order = append(order, message.From)
		}
	}
	if spanish {
		return fmt.Sprintf("mnemo: %d mensaje(s) esperándote, de %s. Léelos con mailbox_read.",
			len(waiting), strings.Join(order, ", "))
	}
	return fmt.Sprintf("mnemo: %d message(s) waiting for you, from %s. Read them with mailbox_read.",
		len(waiting), strings.Join(order, ", "))
}

// nudge is the sentence, in one language or the other.
func nudge(spanish bool, reason string) string {
	if spanish {
		return fmt.Sprintf("mnemo: %s en esta sesión. Considera /mnemo:save-context <proyecto> para no perder "+
			"el avance.", reason)
	}
	return fmt.Sprintf("mnemo: %s this session. Consider /mnemo:save-context <project> so you don't lose "+
		"progress.", reason)
}

func editReason(spanish bool, edits int) string {
	if spanish {
		return fmt.Sprintf("%d ediciones sin guardar", edits)
	}
	if edits == 1 {
		return "1 unsaved edit"
	}
	return fmt.Sprintf("%d unsaved edits", edits)
}

func tokenReason(spanish bool, used int) string {
	if spanish {
		return fmt.Sprintf("contexto en ~%dk tokens", used/1000)
	}
	return fmt.Sprintf("context at ~%dk tokens", used/1000)
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func loadSessionState(path string) sessionState {
	var state sessionState
	raw, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	_ = json.Unmarshal(raw, &state)
	return state
}

func saveSessionState(path string, state sessionState) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, encoded, 0o600)
}

// sweepOldState deletes what belongs to sessions nobody will open again. A
// session file per chat accumulates for ever otherwise.
func sweepOldState(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) <= stateLifetime {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// storeHead is the store's current commit, read from the files in .git.
//
// Without running git, because this is on the path of every edit in somebody's
// session and starting a process there would be felt. What it is used for is
// noticing that a save happened, so a value that is merely stable and changes
// when the history does is enough.
func storeHead(store string) string {
	git := filepath.Join(store, ".git")
	head, err := os.ReadFile(filepath.Join(git, "HEAD"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(head))
	ref, isSymbolic := strings.CutPrefix(line, "ref: ")
	if !isSymbolic {
		// Detached: HEAD is the commit itself.
		return line
	}
	if loose, err := os.ReadFile(filepath.Join(git, filepath.FromSlash(ref))); err == nil {
		return strings.TrimSpace(string(loose))
	}
	// A packed ref, which is what a freshly cloned store has.
	packed, err := os.ReadFile(filepath.Join(git, "packed-refs"))
	if err != nil {
		return ""
	}
	for _, packedLine := range strings.Split(string(packed), "\n") {
		fields := strings.Fields(packedLine)
		if len(fields) == 2 && fields[1] == ref {
			return fields[0]
		}
	}
	return ""
}

// contextTokens is how much context the session is carrying, from the last usage
// entry in the transcript.
//
// The transcript's format is the tool's own and may change, which is why the
// signal that uses this is off by default and why every failure here is silence.
func contextTokens(transcript string) int {
	raw, err := os.ReadFile(transcript)
	if err != nil {
		return 0
	}
	lines := strings.Split(string(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || !strings.Contains(line, "usage") {
			continue
		}
		var entry struct {
			Message struct {
				Usage struct {
					InputTokens              int `json:"input_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		usage := entry.Message.Usage
		if total := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens; total > 0 {
			return total
		}
	}
	return 0
}
