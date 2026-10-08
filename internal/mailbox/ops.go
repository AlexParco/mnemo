package mailbox

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// The operations. Every one of them, except peek, takes the lock, reaps the
// holders that are gone, prunes when a prune is due, and records that the caller
// was seen. Doing that in one place is what keeps "live" honest: a status is
// never inferred from old state.

var kebab = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Sent is the result of sending.
type Sent struct {
	Message Message
	// Warning is said when the message will wait: an address nobody has ever
	// held is a plausible typo, and the alternative to saying so is silence.
	Warning string
}

// Send leaves a message for an address or a group.
func (m *Mailbox) Send(ctx context.Context, caller Caller, to, body, project string) (Sent, error) {
	var sent Sent
	if err := caller.Named(); err != nil {
		return sent, err
	}
	body, err := checkBody(body)
	if err != nil {
		return sent, err
	}

	err = m.operate(ctx, caller, func(state *State, messages []Message, now time.Time) error {
		resolved, warning, err := m.resolve(state, caller, to)
		if err != nil {
			return err
		}
		if project != "" {
			if !kebab.MatchString(project) {
				return refuse("'%s' is not a project slug: use lower-case kebab-case.", project)
			}
			if !m.projectExists(project) {
				return refuse("No project '%s' in the store next to this mailbox. Send it without a project, or "+
					"check the slug with mnemo_list_projects.", project)
			}
		}

		seq := nextSeq(state, messages)
		message := Message{
			Seq: seq, ID: newID(seq), TS: stamp(now),
			From: caller.Address(), Tool: caller.Tool, To: resolved,
			Body: body, Project: project,
		}
		if err := m.append(message); err != nil {
			return err
		}
		state.NextSeq = seq + 1
		sent = Sent{Message: message, Warning: warning}
		return nil
	})
	return sent, err
}

// Reply answers one message. The reply goes to whoever sent it, carries the id it
// answers, and inherits its project.
func (m *Mailbox) Reply(ctx context.Context, caller Caller, id, body string) (Sent, error) {
	var sent Sent
	if err := caller.Named(); err != nil {
		return sent, err
	}
	body, err := checkBody(body)
	if err != nil {
		return sent, err
	}

	err = m.operate(ctx, caller, func(state *State, messages []Message, now time.Time) error {
		var original *Message
		for i := range messages {
			if messages[i].ID == id {
				original = &messages[i]
				break
			}
		}
		if original == nil {
			// Saying how long they are kept turns "not found" into something the
			// agent can explain to a person.
			return refuse("No message '%s'. A message that has been read is kept for %d days, so it may have been "+
				"pruned. Call mailbox_read to see what is waiting.", id, int(Retention.Hours()/24))
		}

		address := caller.Address()
		entry := state.Addresses[address]
		if entry == nil || !wasRecipient(*original, address, entry) {
			return refuse("'%s' was not addressed to %s, so it is not yours to answer.", id, address)
		}

		seq := nextSeq(state, messages)
		message := Message{
			Seq: seq, ID: newID(seq), TS: stamp(now),
			From: address, Tool: caller.Tool, To: original.From,
			Body: body, Project: original.Project, ReplyTo: original.ID,
		}
		if err := m.append(message); err != nil {
			return err
		}
		state.NextSeq = seq + 1
		sent = Sent{Message: message}
		return nil
	})
	return sent, err
}

// wasRecipient reports whether an address was among a message's recipients.
//
// It is not the same question as "is this unread": the cursor has usually moved
// past it by the time anyone replies.
func wasRecipient(message Message, address string, entry *Address) bool {
	if message.From == address {
		return false
	}
	if message.To == address {
		return true
	}
	if !isGroup(message.To) {
		return false
	}
	if message.To != "@all" && "@"+entry.Tool != message.To {
		return false
	}
	// A group reached it only if it already existed when the message was sent.
	return message.Seq > entry.Floor
}

// Delivery is what one read hands over, and what it had to leave.
type Delivery struct {
	Messages []Message
	// Left is how many are still waiting. It is said in the answer, so an agent
	// reads again rather than assuming it has everything.
	Left int
}

// MaxDelivered and MaxDeliveredBytes bound one read.
//
// A message may be 256 KiB and nothing limits how many are waiting, so reading
// everything could be megabytes — into a tool result, which is the agent's
// context. Every other answer mnemo gives is bounded and says where it stopped;
// this is that rule, for the mailbox.
const (
	MaxDelivered      = 20
	MaxDeliveredBytes = 128 * 1024
)

// fit is as many of the waiting messages as one answer may carry.
//
// Always at least one, however large: a message over the budget would otherwise
// sit at the head of the queue for ever, and nothing behind it would move either.
func fit(waiting []Message) Delivery {
	carried := 0
	for i, message := range waiting {
		if i >= MaxDelivered || (i > 0 && carried+len(message.Body) > MaxDeliveredBytes) {
			return Delivery{Messages: waiting[:i], Left: len(waiting) - i}
		}
		carried += len(message.Body)
	}
	return Delivery{Messages: waiting}
}

// Read returns what is waiting for the caller and marks read what it hands over.
func (m *Mailbox) Read(ctx context.Context, caller Caller) (Delivery, error) {
	var out Delivery
	if err := caller.Named(); err != nil {
		return out, err
	}
	err := m.operate(ctx, caller, func(state *State, messages []Message, now time.Time) error {
		address := caller.Address()
		entry := state.Addresses[address]
		waiting := unread(messages, address, entry)
		out = fit(waiting)

		if len(out.Messages) == len(waiting) {
			// Everything went, so the cursor goes to the top of the log rather
			// than to the last message: anything below it was either read, sent
			// by this address, or addressed to a group it is not in, and none of
			// those should come back later.
			if top := highest(messages); top > entry.Cursor {
				entry.Cursor = top
			}
			return nil
		}
		// Only what was handed over is marked read, so the next read starts
		// where this one stopped.
		entry.Cursor = out.Messages[len(out.Messages)-1].Seq
		return nil
	})
	return out, err
}

// Waited is the result of waiting.
type Waited struct {
	Delivery
	// TimedOut says nothing arrived, which is a normal answer and not a failure.
	TimedOut bool
}

// DefaultWait and MaxWait bound a wait. The ceiling is below the minute at which
// Codex cuts a tool call, so a wait always answers before the client gives up.
const (
	DefaultWait = 30 * time.Second
	MaxWait     = 55 * time.Second
	pollEvery   = 250 * time.Millisecond
)

// Wait holds until something arrives for the caller, or the time runs out.
//
// It watches the size of the log rather than polling under the lock, so an idle
// wait costs one stat every quarter second and never keeps another agent out.
func (m *Mailbox) Wait(ctx context.Context, caller Caller, timeout time.Duration) (Waited, error) {
	if err := caller.Named(); err != nil {
		return Waited{}, err
	}
	if timeout <= 0 {
		timeout = DefaultWait
	}
	if timeout > MaxWait {
		timeout = MaxWait
	}

	// Anything already waiting comes back at once.
	// The size is sampled before the read, not after it. A message appended in
	// between would otherwise already be in the size this loop compares against,
	// so nothing would look changed and the wait would run its full length and
	// report that nothing arrived while it was sitting in the file.
	size := m.logSize()
	delivered, err := m.Read(ctx, caller)
	if err != nil || len(delivered.Messages) > 0 {
		return Waited{Delivery: delivered}, err
	}

	deadline := m.now().Add(timeout)
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// A cancelled wait is not a failure: the chat went away.
			return Waited{TimedOut: true}, nil
		case <-ticker.C:
			if current := m.logSize(); current != size {
				size = current
				delivered, err := m.Read(ctx, caller)
				if err != nil {
					return Waited{}, err
				}
				if len(delivered.Messages) > 0 {
					return Waited{Delivery: delivered}, nil
				}
			}
			if !m.now().Before(deadline) {
				return Waited{TimedOut: true}, nil
			}
		}
	}
}

// Peer is one row of what peers reports.
type Peer struct {
	Address string
	Tool    string
	Live    bool
	Unread  int
	// LastSeen is empty for an address that has never been heard from.
	LastSeen time.Time
	Caller   bool
}

// Peers is who the caller can reach.
type Peers struct {
	Machine string
	Peers   []Peer
	// Migrated names the directory an earlier install's files were moved to, said
	// once so a person is not left wondering where their messages went.
	Migrated string
}

// Offline is how long an address with nothing waiting is kept before it is
// forgotten. Somebody who has not opened that chat in three months is not going
// to be reached at that address.
const Offline = 90 * 24 * time.Hour

// List reports every address the caller could write to.
func (m *Mailbox) List(ctx context.Context, caller Caller) (Peers, error) {
	out := Peers{Machine: m.Machine}
	err := m.operate(ctx, caller, func(state *State, messages []Message, now time.Time) error {
		out.Migrated = m.Moved()
		for _, address := range state.Known() {
			entry := state.Addresses[address]
			waiting := len(unread(messages, address, entry))
			// Forgotten only when nothing is waiting: an address with unread
			// messages is somebody who has not come back yet, not somebody gone.
			//
			// And only when nothing in the log is addressed to it at all. Its
			// cursor is the only record that those messages were read: forget it
			// while they are still there and nothing can ever prune them, and a
			// new chat opened under the same name is handed, as new work, what
			// the last one finished three months ago.
			if waiting == 0 && !addressed(messages, address) && entry.Holder == nil &&
				!entry.LastSeen.IsZero() && now.Sub(entry.LastSeen) > Offline {
				delete(state.Addresses, address)
				continue
			}
			out.Peers = append(out.Peers, Peer{
				Address: address, Tool: entry.Tool,
				Live:   entry.Holder != nil,
				Unread: waiting, LastSeen: entry.LastSeen,
				Caller: address == caller.Address(),
			})
		}
		// The caller first, then whoever is there now, then by address: the
		// question this answers is "who can I reach right now".
		sort.SliceStable(out.Peers, func(i, j int) bool {
			left, right := out.Peers[i], out.Peers[j]
			if left.Caller != right.Caller {
				return left.Caller
			}
			if left.Live != right.Live {
				return left.Live
			}
			return left.Address < right.Address
		})
		return nil
	})
	return out, err
}

// Peek is for watchers. It reads the files without the lock, records nobody and
// marks nothing read.
//
// That is safe because state.json is replaced atomically, the log is append-only,
// and a torn last line is skipped. A watcher polls every few seconds; making it
// take the lock would mean an idle machine keeps its own agents waiting.
func (m *Mailbox) Peek(address string, after int) (int, []Message, error) {
	state, err := m.loadState()
	if err != nil {
		return 0, nil, err
	}
	messages, err := m.readLog()
	if err != nil {
		return 0, nil, err
	}
	top := highest(messages)
	entry, known := state.Addresses[address]
	if !known {
		// An address nobody has ever held receives no group messages: it has no
		// floor, so every group message would look new for ever.
		//
		// It is given back the `after` it was asked with, not the top of the
		// log. A watcher starts at the same moment as the chat it watches and
		// can easily look before the name has been claimed; told how far the log
		// goes, it would record itself as having announced messages it never
		// announced, and the chat would sit idle next to post nobody mentioned.
		return after, nil, nil
	}
	from := *entry
	if after > from.Cursor {
		from.Cursor = after
	}
	return top, unread(messages, address, &from), nil
}

// operate is the shape every locked operation shares.
func (m *Mailbox) operate(ctx context.Context, caller Caller, fn func(*State, []Message, time.Time) error) error {
	return m.hold(ctx, func() error {
		if _, err := m.ensureDir(); err != nil {
			return err
		}
		state, err := m.loadState()
		if err != nil {
			return err
		}
		messages, err := m.readLog()
		if err != nil {
			return err
		}
		now := m.now()
		m.reap(state, now)
		if address := caller.Address(); address != "" {
			state.see(address, m.tellable(state, caller), highest(messages), now)
		}

		if err := fn(state, messages, now); err != nil {
			return err
		}
		// Read again before pruning. The operation may have appended, and pruning
		// from the list as it was would rewrite the log without the message that
		// was just sent.
		current, err := m.readLog()
		if err != nil {
			return err
		}
		if pruned, did := m.prune(state, current, now); did {
			if err := m.rewriteLog(pruned); err != nil {
				return err
			}
			state.PrunedAt = now
		}
		return m.saveState(state)
	})
}

// tellable is the tool to record for this caller, which is nothing at all when
// another live instance holds the address.
//
// A caller that does not hold a name may still be heard from — `peers` answers
// for a chat whose claim was refused, because that is how a user finds out which
// chat has the name. What it may not do is retag the address: the tool on an
// address decides which group messages reach it, so a refused chat in Codex
// calling peers would quietly take `@claude` away from the chat that holds the
// name and point `@codex` at it. Claim guards this for itself; every other
// operation goes through here.
func (m *Mailbox) tellable(state *State, caller Caller) string {
	entry, known := state.Addresses[caller.Address()]
	if known && entry.Holder != nil && entry.Holder.Instance != caller.Instance {
		return ""
	}
	return caller.Tool
}

// prune drops what nobody needs any more, at most once a day.
//
// A message is only removed when everyone who was going to read it has: a
// message nobody has read is never removed, however old, because the agent it
// was for may simply not have been opened since.
func (m *Mailbox) prune(state *State, messages []Message, now time.Time) ([]Message, bool) {
	if len(messages) == 0 || now.Sub(state.PrunedAt) < 24*time.Hour {
		return nil, false
	}
	cutoff := now.Add(-Retention)
	kept := make([]Message, 0, len(messages))
	dropped := 0
	for _, message := range messages {
		sent, err := time.Parse("2006-01-02T15:04:05.000Z", message.TS)
		if err != nil || sent.After(cutoff) {
			kept = append(kept, message)
			continue
		}
		if isGroup(message.To) || readBy(state, message) {
			dropped++
			continue
		}
		kept = append(kept, message)
	}
	return kept, dropped > 0
}

// addressed reports whether any message in the log was sent to one address.
func addressed(messages []Message, address string) bool {
	for _, message := range messages {
		if message.To == address {
			return true
		}
	}
	return false
}

// readBy reports whether the address a message was sent to has read past it.
func readBy(state *State, message Message) bool {
	entry, known := state.Addresses[message.To]
	return known && entry.Cursor >= message.Seq
}

// resolve turns what a caller asked for into what is stored.
func (m *Mailbox) resolve(state *State, caller Caller, to string) (string, string, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return "", "", refuse("Say who this is for: an address like name@machine, or a group like @all.")
	}

	if isGroup(to) {
		group := strings.ToLower(to)
		if group == "@all" || slices.Contains(Tools, strings.TrimPrefix(group, "@")) {
			return group, "", nil
		}
		return "", "", refuse("'%s' is not a group. The groups are @all, %s.", to,
			"@"+strings.Join(Tools, ", @"))
	}

	if address, machine, split := strings.Cut(to, "@"); split {
		if !IsName(address) || machine == "" {
			return "", "", refuse("'%s' is not an address. They look like name@machine.", to)
		}
		if to == caller.Address() {
			return "", "", refuse("That is you. A message to yourself would only come back.")
		}
		if _, known := state.Addresses[to]; !known {
			return to, fmt.Sprintf("Nobody has used %s yet, so this will wait until somebody does. Check the "+
				"spelling with mailbox_peers if that is not what you meant.", to), nil
		}
		return to, "", nil
	}

	// A bare name, which is the convenient form and the ambiguous one.
	var matches []string
	for _, address := range state.Known() {
		if name, _, _ := strings.Cut(address, "@"); name == to {
			matches = append(matches, address)
		}
	}
	switch len(matches) {
	case 0:
		return "", "", refuse("Nobody called '%s' has appeared yet. Use name@machine to leave a message for an "+
			"agent that has not been opened, or call mailbox_peers to see who is there.", to)
	case 1:
		if matches[0] == caller.Address() {
			return "", "", refuse("That is you. A message to yourself would only come back.")
		}
		return matches[0], "", nil
	default:
		return "", "", refuse("There is more than one '%s': %s. Say which one.", to, strings.Join(matches, ", "))
	}
}

// checkBody trims a body and refuses one that says nothing or too much.
func checkBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", refuse("The message is empty. Say what you want the other agent to know.")
	}
	if len(body) > MaxBody {
		return "", refuse("The message is %d bytes and the limit is %d. A mailbox is for a sentence, not for "+
			"moving files: put the content somewhere and send the path.", len(body), MaxBody)
	}
	return body, nil
}
