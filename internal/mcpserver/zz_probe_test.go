package mcpserver

import "testing"

func TestProbeRefusedChatIsToldItHasPost(t *testing.T) {
	calls := chats(t, "alex", "alex", "sender")
	first, second, sender := calls[0], calls[1], calls[2]
	if _, err := first.box.Claim(t.Context(), first.Caller()); err != nil {
		t.Fatal(err)
	}
	_, err := second.box.Claim(t.Context(), second.Caller())
	if err == nil {
		t.Fatal("name given twice")
	}
	second.ClaimRefusal = err.Error()

	structured(t, handleSend, sender, Args{"to": "alex@here", "body": "for the holder only"})

	t.Logf("first.waiting()  = %q", first.waiting())
	t.Logf("second.waiting() = %q", second.waiting())

	// what the refused chat gets when it then tries to read
	said := refusedBy(t, handleRead, second, Args{})
	t.Logf("second read -> %q", said)
}
