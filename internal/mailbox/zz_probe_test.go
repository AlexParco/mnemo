package mailbox

import (
	"strings"
	"testing"
	"time"
)

func TestProbeForgottenAddressRedelivers(t *testing.T) {
	b := newBox(t)
	caller := b.agent(t, "worker", "claude", 100)
	gone := b.agent(t, "gone", "codex", 200)

	b.send(t, caller, "gone@here", "read this")
	if got := bodies(b.read(t, gone)); got != "read this" {
		t.Fatalf("first read %q", got)
	}

	b.alive[200] = false
	b.now = b.now.Add(Offline + time.Hour)
	if _, err := b.List(t.Context(), caller); err != nil {
		t.Fatal(err)
	}

	messages, err := b.readLog()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("log after forget+prune: %q", bodies(messages))
	if !strings.Contains(bodies(messages), "read this") {
		t.Logf("message was pruned: no redelivery")
		return
	}

	// The chat reopens under the same name.
	again := b.agent(t, "gone", "codex", 201)
	t.Logf("second read: %q", bodies(b.read(t, again)))
}
