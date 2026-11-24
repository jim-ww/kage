package xmpp

import (
	"testing"
	"time"
)

func TestSentAt(t *testing.T) {
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	stamp := func(s string) delayElem { return delayElem{Stamp: s} }
	for _, tc := range []struct {
		name   string
		delays []delayElem
		want   time.Time
	}{
		{"none is now", nil, now},
		{"one", []delayElem{stamp("2026-11-20T13:20:05Z")}, time.Date(2026, 11, 20, 13, 20, 5, 0, time.UTC)},
		{"fractional seconds", []delayElem{stamp("2026-11-20T13:20:05.250Z")}, time.Date(2026, 11, 20, 13, 20, 5, 250e6, time.UTC)},
		{"offset", []delayElem{stamp("2026-11-20T14:20:05+01:00")}, time.Date(2026, 11, 20, 13, 20, 5, 0, time.UTC)},
		{"earliest of several", []delayElem{stamp("2026-11-20T13:50:00Z"), stamp("2026-11-20T13:20:00Z")}, time.Date(2026, 11, 20, 13, 20, 0, 0, time.UTC)},
		{"future clamped to now", []delayElem{stamp("2026-11-20T15:00:00Z")}, now},
		{"unparseable ignored", []delayElem{stamp("yesterday"), stamp(""), stamp("2026-11-20T13:20:00Z")}, time.Date(2026, 11, 20, 13, 20, 0, 0, time.UTC)},
		{"only unparseable is now", []delayElem{stamp("20261120T13:20:00")}, now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sentAt(now, tc.delays); !got.Equal(tc.want) {
				t.Errorf("sentAt = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLiveMessageSentAtFromDelay checks a message held on its way - by our
// server while offline, or forwarded as a carbon - is dated by its delay
// rather than by when it reached us.
func TestLiveMessageSentAtFromDelay(t *testing.T) {
	want := time.Date(2026, 11, 20, 13, 20, 5, 0, time.UTC)
	for _, tc := range []struct{ name, payload string }{
		{"offline storage", `<message xmlns="jabber:client" from="bob@example.test/pc" to="alice@example.test/laptop" type="chat" id="m1">
			<body>hi</body>
			<delay xmlns="urn:xmpp:delay" from="example.test" stamp="2026-11-20T13:20:05Z">Offline Storage</delay>
		</message>`},
		{"carbon, delay on forwarded", `<message xmlns="jabber:client" from="alice@example.test" to="alice@example.test/laptop">
			<received xmlns="urn:xmpp:carbons:2"><forwarded xmlns="urn:xmpp:forward:0">
				<delay xmlns="urn:xmpp:delay" stamp="2026-11-20T13:20:05Z"/>
				<message xmlns="jabber:client" from="bob@example.test/pc" to="alice@example.test/phone" type="chat" id="m1"><body>hi</body></message>
			</forwarded></received>
		</message>`},
		{"carbon, delay on wrapper and inner", `<message xmlns="jabber:client" from="alice@example.test" to="alice@example.test/laptop">
			<delay xmlns="urn:xmpp:delay" from="example.test" stamp="2026-11-20T13:40:00Z"/>
			<received xmlns="urn:xmpp:carbons:2"><forwarded xmlns="urn:xmpp:forward:0">
				<message xmlns="jabber:client" from="bob@example.test/pc" to="alice@example.test/phone" type="chat" id="m1">
					<body>hi</body>
					<delay xmlns="urn:xmpp:delay" stamp="2026-11-20T13:20:05Z"/>
				</message>
			</forwarded></received>
		</message>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, events := serveMessage(t, tc.payload)
			msgs := messages(events)
			if len(msgs) != 1 {
				t.Fatalf("%d messages, want 1", len(msgs))
			}
			if !msgs[0].SentAt.Equal(want) {
				t.Errorf("SentAt = %v, want %v", msgs[0].SentAt, want)
			}
		})
	}

	_, events := serveMessage(t, `<message xmlns="jabber:client" from="bob@example.test/pc" to="alice@example.test/laptop" type="chat" id="m2"><body>hi</body></message>`)
	if msgs := messages(events); len(msgs) != 1 || time.Since(msgs[0].SentAt) > time.Minute {
		t.Errorf("undelayed message: %+v, want it dated now", msgs)
	}
}

// TestArchiveResultSentAt checks an archived item's stamp is clamped to now
// like a live one's, and that a missing one stays unknown (zero) for the
// caller rather than turning into now.
func TestArchiveResultSentAt(t *testing.T) {
	result := func(delay string) *mamResultElem {
		r := &mamResultElem{QueryID: "q1", ID: "a1"}
		r.Forwarded.Delay.Stamp = delay
		r.Forwarded.Message.Body = "hi"
		return r
	}
	dispatch := func(r *mamResultElem) ArchivedMessage {
		t.Helper()
		c := &Client{}
		ch := make(chan ArchivedMessage, 1)
		c.mamWaiters = map[string]chan ArchivedMessage{"q1": ch}
		c.dispatchArchiveResult(r)
		select {
		case am := <-ch:
			return am
		default:
			t.Fatal("item not delivered")
			return ArchivedMessage{}
		}
	}

	if am := dispatch(result("2026-11-20T13:20:05Z")); !am.SentAt.Equal(time.Date(2026, 11, 20, 13, 20, 5, 0, time.UTC)) {
		t.Errorf("stamped: SentAt = %v", am.SentAt)
	}
	if am := dispatch(result(time.Now().Add(time.Hour).UTC().Format(time.RFC3339))); am.SentAt.After(time.Now()) {
		t.Errorf("future stamp not clamped: SentAt = %v", am.SentAt)
	}
	if am := dispatch(result("")); !am.SentAt.IsZero() {
		t.Errorf("no stamp: SentAt = %v, want zero", am.SentAt)
	}
}
