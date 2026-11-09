package xmpp

import (
	"encoding/xml"
	"testing"

	"mellium.im/xmpp/stanza"
)

// ejabberdBounce is how ejabberd bounces a chat state we sent to an
// unreachable contact: from them, type error, our own <composing/> still
// inside.
const ejabberdBounce = `<message xmlns="jabber:client" type="error" from="bob@example.org" to="alice@example.org/kage" id="cs1">
  <composing xmlns="http://jabber.org/protocol/chatstates"/>
  <body>our own text</body>
  <error type="cancel"><service-unavailable xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/>
    <text xmlns="urn:ietf:params:xml:ns:xmpp-stanzas">User session not found</text></error>
</message>`

func TestErrorBounceDecodes(t *testing.T) {
	var msg messageBody
	if err := xml.Unmarshal([]byte(ejabberdBounce), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != stanza.ErrorMessage || msg.ID != "cs1" {
		t.Fatalf("type %q id %q", msg.Type, msg.ID)
	}
	// What the bounce check in handleStanza exists for: the payload reads
	// like a chat state, so it must be caught before chatState() looks.
	if _, ok := msg.chatState(); !ok {
		t.Fatal("fixture should carry a chat state")
	}
	if got := bounceReason(msg.Error); got != "service unavailable: User session not found" {
		t.Fatalf("reason %q", got)
	}
}

func TestBounceReasonFallbacks(t *testing.T) {
	if got := bounceReason(nil); got != "undeliverable" {
		t.Fatalf("nil error: %q", got)
	}
	if got := bounceReason(&stanza.Error{Condition: stanza.RemoteServerNotFound}); got != "remote server not found" {
		t.Fatalf("condition only: %q", got)
	}
}
