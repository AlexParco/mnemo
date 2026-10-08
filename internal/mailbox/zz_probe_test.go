package mailbox

import (
	"testing"
	"time"
)

func TestProbeForgottenAddressRedelivery(t *testing.T) {
	b := newBox(t)
	worker := b.agent(t, "worker", "claude", 100)
	gone := b.agent(t, "gone", "codex", 200)

	b.send(t, worker, "gone@here", "read this")
	got := b.read(t, gone)
	if len(got) != 1 {
		t.Fatalf("gone read %d messages, want 1", len(got))
	}
	// gone's process dies
	b.alive[200] = false

	// clock moves past Offline
	b.now = b.now.Add(Offline + 48*time.Hour)

	peers, err := b.List(t.Context(), worker)
	if err != nil {
		t.Fatalf("peers: %v", err)
	}
	for _, p := range peers.Peers {
		t.Logf("peer %s live=%v unread=%d", p.Address, p.Live, p.Unread)
	}

	st, err := b.loadState()
	if err != nil {
		t.Fatal(err)
	}
	_, stillKnown := st.Addresses["gone@here"]
	log, err := b.readLog()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("gone@here still in state: %v; log has %d messages", stillKnown, len(log))
	for _, msg := range log {
		t.Logf("  log: seq=%d to=%s body=%q", msg.Seq, msg.To, msg.Body)
	}

	// a new chat claims the same name
	fresh := b.agent(t, "gone", "codex", 300)
	again, err := b.Read(t.Context(), fresh)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fresh gone@here re-read %d messages", len(again.Messages))
	if len(again.Messages) > 0 {
		t.Errorf("REPRODUCED: re-delivered %q", again.Messages[0].Body)
	}
}
