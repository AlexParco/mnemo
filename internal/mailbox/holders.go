package mailbox

import (
	"context"
	"fmt"
	"time"
)

// Holding a name.
//
// One address, at most one live instance. That is the whole point: a reply has to
// find exactly one chat, and "who is worker-api" must have one answer. Everything
// else here follows from it.

// Claim takes an address for the caller, or says who has it.
//
// It happens when an instance starts, before it can be asked anything — a chat
// that never touches the mailbox still holds its name, so that opening a second
// chat with the same name fails at the moment somebody tries, rather than later
// when a reply goes to the wrong one.
func (m *Mailbox) Claim(ctx context.Context, caller Caller) (Held, error) {
	var held Held
	if err := caller.Named(); err != nil {
		return held, err
	}
	err := m.hold(ctx, func() error {
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

		address := caller.Address()
		// The holder is checked before the address records this caller: seeing it
		// first would overwrite the tool, and the refusal would name the tool of
		// the chat being turned away rather than the one that has the name.
		if existing, known := state.Addresses[address]; known &&
			existing.Holder != nil && existing.Holder.Instance != caller.Instance {
			// Named, with the tool and the time: "already taken" is not enough
			// for somebody deciding whether to close a chat or rename this one.
			return refuse("%s is already open in %s. Close that chat, or pick another name.",
				address, describe(existing))
		}
		entry := state.see(address, caller.Tool, highest(messages), now)

		holder := &Holder{Instance: caller.Instance, Via: caller.Via, PID: caller.PID, Since: now}
		if entry.Holder != nil {
			// Renewing: keep when it started, so `peers` says how long the chat
			// has been open rather than when it last spoke.
			holder.Since = entry.Holder.Since
		}
		if caller.Via == "remote" {
			holder.LeaseUntil = now.Add(Lease)
		}
		entry.Holder = holder
		held = Held{Address: address, Since: holder.Since, LeaseUntil: holder.LeaseUntil}
		return m.saveState(state)
	})
	return held, err
}

// Held is what a claim gives back.
type Held struct {
	Address string
	Since   time.Time
	// LeaseUntil is set for a remote holder, which has to keep renewing.
	LeaseUntil time.Time
}

// Release gives up a name, when a process exits cleanly. A crash is covered by
// the process check or by the lease, so this is a courtesy rather than the
// mechanism.
func (m *Mailbox) Release(ctx context.Context, caller Caller) error {
	if caller.Address() == "" {
		return nil
	}
	return m.hold(ctx, func() error {
		state, err := m.loadState()
		if err != nil {
			return err
		}
		entry, known := state.Addresses[caller.Address()]
		if !known || entry.Holder == nil || entry.Holder.Instance != caller.Instance {
			return nil
		}
		entry.Holder = nil
		return m.saveState(state)
	})
}

// HeldBy says who holds an address, and whether they are alive, without claiming
// anything. It is what `mnemo run` asks before it opens a chat.
func (m *Mailbox) HeldBy(ctx context.Context, address string) (Holder, bool, error) {
	var holder Holder
	var live bool
	err := m.hold(ctx, func() error {
		state, err := m.loadState()
		if err != nil {
			return err
		}
		entry, known := state.Addresses[address]
		if !known || entry.Holder == nil {
			return nil
		}
		if !m.holderAlive(entry.Holder, m.now()) {
			return nil
		}
		holder, live = *entry.Holder, true
		return nil
	})
	return holder, live, err
}

// Describe is how a live holder is named to a person: the tool it runs in, when
// it opened, and whether it is on another machine. It is empty when nobody holds
// the address.
//
// It exists so that `mnemo run`, which checks before it opens a chat, refuses in
// the same words the claim itself would — and above all with the same facts. The
// holder record carries no tool, so a command reading only that could say no more
// than "another chat", which is of no use to somebody deciding which window to
// close.
func (m *Mailbox) Describe(ctx context.Context, address string) (string, error) {
	var said string
	err := m.hold(ctx, func() error {
		state, err := m.loadState()
		if err != nil {
			return err
		}
		entry, known := state.Addresses[address]
		if !known || entry.Holder == nil || !m.holderAlive(entry.Holder, m.now()) {
			return nil
		}
		said = describe(entry)
		return nil
	})
	return said, err
}

// describe is how a refusal names the chat that already has a name: the tool it
// runs in, when it opened, and whether it is on another machine.
func describe(entry *Address) string {
	tool := entry.Tool
	if tool == "" {
		tool = "another chat"
	}
	where := ""
	if entry.Holder != nil && entry.Holder.Via == "remote" {
		where = " on another machine"
	}
	return fmt.Sprintf("%s%s since %s", tool, where, entry.Holder.Since.Local().Format("15:04"))
}
