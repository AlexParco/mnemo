package mailbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"
)

// Version is the shape of state.json. A file without it was written by an
// earlier install and is moved aside rather than read.
const Version = 2

// Holder is the one live instance that has an address.
type Holder struct {
	Instance string `json:"instance"`
	// Via is "local" or "remote", which decides how liveness is judged.
	Via string `json:"via"`
	// PID is checked for a local holder, on the machine hosting the mailbox.
	PID   int       `json:"pid,omitempty"`
	Since time.Time `json:"since"`
	// LeaseUntil is when a remote holder stops being alive unless it is renewed.
	LeaseUntil time.Time `json:"lease_until,omitempty"`
}

// Address is what is remembered about one address between operations.
type Address struct {
	Tool string `json:"tool"`
	// Floor is the highest seq at the moment this address first became known.
	// Group messages from before it existed never reach it: an agent opened today
	// is not answerable for a conversation it was never part of.
	Floor int `json:"floor"`
	// Cursor is the highest seq this address has read. It belongs to the address
	// and not to a process, so a chat that restarts under the same name carries
	// on where it left off.
	Cursor   int       `json:"cursor"`
	LastSeen time.Time `json:"last_seen"`
	Holder   *Holder   `json:"holder,omitempty"`
}

// State is state.json.
type State struct {
	Version int `json:"version"`
	// NextSeq is the seq the next message will take.
	NextSeq   int                 `json:"next_seq"`
	PrunedAt  time.Time           `json:"pruned_at,omitempty"`
	Addresses map[string]*Address `json:"addresses"`
}

// loadState reads state.json, or returns an empty one.
func (m *Mailbox) loadState() (*State, error) {
	state := &State{Version: Version, NextSeq: 1, Addresses: map[string]*Address{}}
	raw, err := os.ReadFile(m.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", m.statePath(), err)
	}
	if err := json.Unmarshal(raw, state); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", m.statePath(), err)
	}
	if state.Addresses == nil {
		state.Addresses = map[string]*Address{}
	}
	if state.NextSeq < 1 {
		state.NextSeq = 1
	}
	return state, nil
}

func (m *Mailbox) saveState(state *State) error {
	state.Version = Version
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the mailbox state: %w", err)
	}
	return writeFile(m.statePath(), append(encoded, '\n'))
}

// Known lists the addresses in state, sorted.
func (s *State) Known() []string {
	out := make([]string, 0, len(s.Addresses))
	for address := range s.Addresses {
		out = append(out, address)
	}
	sort.Strings(out)
	return out
}

// see records that an address exists and was heard from.
//
// The first time, its floor is set to the highest seq there is: it starts
// answerable for what comes next, not for everything that came before.
func (s *State) see(address, tool string, highest int, now time.Time) *Address {
	entry, known := s.Addresses[address]
	if !known {
		entry = &Address{Floor: highest, Cursor: 0}
		s.Addresses[address] = entry
	}
	if tool != "" {
		entry.Tool = tool
	}
	entry.LastSeen = now
	return entry
}

// alive reports whether an address has a holder that is still there.
//
// A local holder is judged by its process; a remote one by its lease. Neither is
// inferred from old state, because the whole value of `live` is that somebody
// checked.
func (m *Mailbox) holderAlive(holder *Holder, now time.Time) bool {
	if holder == nil {
		return false
	}
	if holder.Via == "remote" {
		return now.Before(holder.LeaseUntil)
	}
	return holder.PID > 0 && m.alive(holder.PID)
}

// reap drops the holders that are gone, so `live` never reports a chat that was
// closed and a name is never held by nothing.
func (m *Mailbox) reap(state *State, now time.Time) {
	for _, entry := range state.Addresses {
		if entry.Holder != nil && !m.holderAlive(entry.Holder, now) {
			entry.Holder = nil
		}
	}
}

// Lease is how long a remote holder lives without a heartbeat. The relay beats
// every 15 seconds, so a client that vanishes loses its name within 45.
const Lease = 45 * time.Second
