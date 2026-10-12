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
				address, Occupant{
					Tool: existing.Tool, Via: existing.Holder.Via, Since: existing.Holder.Since,
				}.Describe())
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

// Occupant is who holds an address, for whoever has to say so: a person
// choosing which window to close, or another machine asking over HTTP.
//
// It carries the tool, which the holder record does not: the tool belongs to the
// address and the holder to the instance, and anybody answering "who has this
// name" needs both. Without it the only honest answer is "another chat", which
// is of no use to somebody deciding what to close.
type Occupant struct {
	Tool string
	// Via is "local" or "remote", which is why a holder can be on another
	// machine and still hold a name here.
	Via string
	// Instance is which process holds it, so that "is this still me" has an
	// answer. It is never shown to anybody: it is 16 random characters.
	Instance string
	Since    time.Time
}

// Describe is the occupant in the words a refusal uses.
func (o Occupant) Describe() string {
	tool := o.Tool
	if tool == "" {
		tool = "another chat"
	}
	where := ""
	if o.Via == "remote" {
		where = " on another machine"
	}
	return fmt.Sprintf("%s%s since %s", tool, where, o.Since.Local().Format("15:04"))
}

// Occupied says who holds an address and whether they are alive, without
// claiming anything.
//
// One question with one answer: `mnemo run` asks it before opening a chat, and
// the server answers /v1/mailbox/holder with it so another machine's `mnemo run`
// can ask the same thing. Liveness is checked rather than remembered.
func (m *Mailbox) Occupied(ctx context.Context, address string) (Occupant, bool, error) {
	var occupant Occupant
	var live bool
	err := m.hold(ctx, func() error {
		state, err := m.loadState()
		if err != nil {
			return err
		}
		entry, known := state.Addresses[address]
		if !known || entry.Holder == nil || !m.holderAlive(entry.Holder, m.now()) {
			return nil
		}
		occupant = Occupant{
			Tool: entry.Tool, Via: entry.Holder.Via,
			Instance: entry.Holder.Instance, Since: entry.Holder.Since,
		}
		live = true
		return nil
	})
	return occupant, live, err
}

// ReleaseInstance gives back whatever name an instance holds, which is at most
// one.
//
// Release takes a caller because a chat knows its own name. A server releasing
// on behalf of a client that has gone knows only the instance — that is all the
// client sends — so the address is found rather than given.
func (m *Mailbox) ReleaseInstance(ctx context.Context, instance string) error {
	if instance == "" {
		return nil
	}
	return m.hold(ctx, func() error {
		state, err := m.loadState()
		if err != nil {
			return err
		}
		for _, entry := range state.Addresses {
			if entry.Holder != nil && entry.Holder.Instance == instance {
				entry.Holder = nil
				return m.saveState(state)
			}
		}
		return nil
	})
}
