package mailbox

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

// Message is one message, as it is stored: one JSON object per line.
type Message struct {
	Seq int    `json:"seq"`
	ID  string `json:"id"`
	TS  string `json:"ts"`
	// From is the sender's full address.
	From string `json:"from"`
	Tool string `json:"tool"`
	// To is always stored resolved: a full address, or a group.
	To      string `json:"to"`
	Body    string `json:"body"`
	Project string `json:"project,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// MaxBody is the most a message may carry. A mailbox is for "the coupons
// endpoint is done", not for moving files.
const MaxBody = 256 * 1024

// Retention is how long a message is kept once it has been read.
const Retention = 30 * 24 * time.Hour

// newID is `m`, the seq in base 36, and four random hex characters. Short enough
// to say out loud, unique enough to reply to.
func newID(seq int) string {
	suffix := make([]byte, 2)
	rand.Read(suffix)
	return "m" + strconv.FormatInt(int64(seq), 36) + hex.EncodeToString(suffix)
}

// stamp is UTC with milliseconds.
func stamp(now time.Time) string {
	return now.UTC().Format("2006-01-02T15:04:05.000Z")
}

// readLog reads every message in the log.
//
// A line that cannot be parsed is skipped rather than fatal. The log is
// append-only and a crash mid-append can leave a torn last line; losing that one
// message is the cost of never losing the rest.
func (m *Mailbox) readLog() ([]Message, error) {
	file, err := os.Open(m.logPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", m.logPath(), err)
	}
	defer file.Close()

	var messages []Message
	scanner := bufio.NewScanner(file)
	// A message may be up to 256 KiB, and the default token size is 64 KiB.
	scanner.Buffer(make([]byte, 0, 64*1024), MaxBody+4096)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message Message
		if err := json.Unmarshal([]byte(line), &message); err != nil || message.Seq <= 0 {
			continue
		}
		messages = append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", m.logPath(), err)
	}
	return messages, nil
}

// highest is the largest seq in the log.
func highest(messages []Message) int {
	top := 0
	for _, message := range messages {
		if message.Seq > top {
			top = message.Seq
		}
	}
	return top
}

// nextSeq is the seq a new message takes.
//
// It is the larger of the stored counter and the highest seq in the log, plus
// one. After a crash the counter can be behind what was appended, and reusing a
// seq would make one message shadow another in every cursor.
func nextSeq(state *State, messages []Message) int {
	next := state.NextSeq
	if fromLog := highest(messages) + 1; fromLog > next {
		next = fromLog
	}
	return next
}

// append adds one message to the log.
//
// It starts on a new line when the file does not end with one, so a line torn by
// a crash cannot swallow the message that follows it.
func (m *Mailbox) append(message Message) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encoding the message: %w", err)
	}

	// Read-write, because the last byte has to be looked at: a file opened
	// write-only cannot be read, and the check below would silently never fire —
	// which is how a torn line ends up swallowing the message after it.
	file, err := os.OpenFile(m.logPath(), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", m.logPath(), err)
	}
	defer file.Close()

	prefix := ""
	if info, err := file.Stat(); err == nil && info.Size() > 0 {
		tail := make([]byte, 1)
		if _, err := file.ReadAt(tail, info.Size()-1); err == nil && tail[0] != '\n' {
			prefix = "\n"
		}
	}
	if _, err := file.Write(append(append([]byte(prefix), encoded...), '\n')); err != nil {
		return fmt.Errorf("appending to %s: %w", m.logPath(), err)
	}
	return nil
}

// rewriteLog replaces the log, for a prune. Atomic, because a watcher reads it
// without the lock.
func (m *Mailbox) rewriteLog(messages []Message) error {
	var out strings.Builder
	for _, message := range messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			return err
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	return writeFile(m.logPath(), []byte(out.String()))
}

// reaches reports whether a message was addressed to one address.
//
// A group reaches an address only when that address was already known when the
// message was sent: the floor is what keeps a chat opened today out of a
// conversation it was never part of. A message never reaches its own sender.
func reaches(message Message, address string, entry *Address) bool {
	if message.From == address {
		return false
	}
	if message.To == address {
		return message.Seq > entry.Cursor
	}
	if !isGroup(message.To) {
		return false
	}
	if message.To != "@all" && "@"+entry.Tool != message.To {
		return false
	}
	return message.Seq > entry.Cursor && message.Seq > entry.Floor
}

// isGroup reports whether an address is a group rather than one agent.
func isGroup(to string) bool { return strings.HasPrefix(to, "@") }

// unread are the messages an address has not seen, in seq order.
func unread(messages []Message, address string, entry *Address) []Message {
	var out []Message
	for _, message := range messages {
		if reaches(message, address, entry) {
			out = append(out, message)
		}
	}
	return out
}
