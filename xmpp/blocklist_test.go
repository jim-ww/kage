package xmpp

import (
	"encoding/xml"
	"slices"
	"strings"
	"testing"
)

func decodeBlockPush(t *testing.T, payload string) BlocklistPushEvent {
	t.Helper()
	var p blockPush
	if err := xml.NewDecoder(strings.NewReader(payload)).Decode(&p); err != nil {
		t.Fatalf("decoding blocklist push: %v", err)
	}
	return blocklistPushEvent(p)
}

func TestBlocklistPushDecodes(t *testing.T) {
	ev := decodeBlockPush(t, `<block xmlns="urn:xmpp:blocking"><item jid="spam@example.test"/><item jid="bad@example.test"/></block>`)
	if !ev.Blocked || !slices.Equal(ev.JIDs, []string{"spam@example.test", "bad@example.test"}) {
		t.Errorf("block push = %+v", ev)
	}
	ev = decodeBlockPush(t, `<unblock xmlns="urn:xmpp:blocking"><item jid="spam@example.test"/></unblock>`)
	if ev.Blocked || !slices.Equal(ev.JIDs, []string{"spam@example.test"}) {
		t.Errorf("unblock push = %+v", ev)
	}
	// Empty: everything unblocked.
	ev = decodeBlockPush(t, `<unblock xmlns="urn:xmpp:blocking"/>`)
	if ev.Blocked || len(ev.JIDs) != 0 {
		t.Errorf("unblock-all push = %+v", ev)
	}
}
