package xmpp

import (
	"bytes"
	"encoding/xml"
	"slices"
	"strings"
	"sync"
	"testing"

	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/stanza"
)

// servePresence runs handleStanza on an incoming <presence/> as alice's
// client would, returning the events it emitted.
func servePresence(t *testing.T, payload string) []Event {
	t.Helper()
	c := &Client{JID: jid.MustParse("alice@example.test/laptop")}
	c.evCond = sync.NewCond(&c.evMu)
	dec := xml.NewDecoder(strings.NewReader(payload))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("reading payload start: %v", err)
	}
	start := tok.(xml.StartElement)
	var out bytes.Buffer
	c.handleStanza(iqStream{dec, xml.NewEncoder(&out)}, &start)
	return c.evBuf
}

// TestGroupchatMessage checks a message in a room comes out as one, from
// its occupant JID, with the room's stanza-id, and that its receipt request
// isn't answered: every occupant would (XEP-0184 §5.3).
func TestGroupchatMessage(t *testing.T) {
	reply, events := serveMessage(t, `<message xmlns="jabber:client" from="room@muc.example.test/bob" to="alice@example.test/laptop" type="groupchat" id="g1">
		<body>hi all</body>
		<request xmlns="urn:xmpp:receipts"/>
		<stanza-id xmlns="urn:xmpp:sid:0" id="arch1" by="room@muc.example.test"/>
	</message>`)
	if strings.Contains(reply, "receipts") {
		t.Errorf("receipt answered in a room: %q", reply)
	}
	msgs := messages(events)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	if !m.Groupchat || m.From != "room@muc.example.test/bob" || m.Body != "hi all" || m.RoomStanzaID != "arch1" || m.ID != "g1" {
		t.Errorf("message = %+v", m)
	}
}

// TestGroupchatStanzaIDOnlyFromRoom checks a stanza-id the room didn't
// assign is ignored: anyone can put one in.
func TestGroupchatStanzaIDOnlyFromRoom(t *testing.T) {
	_, events := serveMessage(t, `<message xmlns="jabber:client" from="room@muc.example.test/bob" type="groupchat">
		<body>hi</body>
		<stanza-id xmlns="urn:xmpp:sid:0" id="forged" by="room@muc.example.test/bob"/>
	</message>`)
	msgs := messages(events)
	if len(msgs) != 1 || msgs[0].RoomStanzaID != "" || msgs[0].ID != "" {
		t.Errorf("messages = %+v", msgs)
	}
}

// TestGroupchatIDFallsBackToStanzaID checks a room message with no id of
// its own is known by the room's, so seeing it again on a rejoin dedupes.
func TestGroupchatIDFallsBackToStanzaID(t *testing.T) {
	_, events := serveMessage(t, `<message xmlns="jabber:client" from="room@muc.example.test/bob" type="groupchat">
		<body>hi</body>
		<stanza-id xmlns="urn:xmpp:sid:0" id="arch2" by="room@muc.example.test"/>
	</message>`)
	if msgs := messages(events); len(msgs) != 1 || msgs[0].ID != "arch2" {
		t.Errorf("messages = %+v", msgs)
	}
}

// TestMUCPrivateMessage checks an occupant's private message is marked so,
// not taken for a contact's.
func TestMUCPrivateMessage(t *testing.T) {
	_, events := serveMessage(t, `<message xmlns="jabber:client" from="room@muc.example.test/bob" type="chat" id="p1">
		<body>psst</body>
		<x xmlns="http://jabber.org/protocol/muc#user"/>
	</message>`)
	if msgs := messages(events); len(msgs) != 1 || !msgs[0].MUCPrivate || msgs[0].Groupchat {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestRoomSubject(t *testing.T) {
	for payload, want := range map[string]string{
		`<message xmlns="jabber:client" from="room@muc.example.test/bob" type="groupchat"><subject>plans</subject></message>`: "plans",
		`<message xmlns="jabber:client" from="room@muc.example.test" type="groupchat"><subject/></message>`:                   "",
	} {
		_, events := serveMessage(t, payload)
		if len(events) != 1 {
			t.Fatalf("%s: events = %+v", payload, events)
		}
		ev, ok := events[0].(RoomSubjectEvent)
		if !ok || ev.Room != "room@muc.example.test" || ev.Subject != want {
			t.Errorf("%s: event = %+v, want subject %q", payload, events[0], want)
		}
	}
}

func TestRoomInvites(t *testing.T) {
	for name, payload := range map[string]string{
		"mediated": `<message xmlns="jabber:client" from="room@muc.example.test" to="alice@example.test">
			<x xmlns="http://jabber.org/protocol/muc#user"><invite from="bob@example.test/pc"><reason>join us</reason></invite><password>pw</password></x>
		</message>`,
		"direct": `<message xmlns="jabber:client" from="bob@example.test/pc" to="alice@example.test">
			<x xmlns="jabber:x:conference" jid="room@muc.example.test" reason="join us" password="pw"/>
		</message>`,
	} {
		_, events := serveMessage(t, payload)
		want := RoomInviteEvent{Room: "room@muc.example.test", From: "bob@example.test", Reason: "join us", Password: "pw"}
		if len(events) != 1 || events[0] != want {
			t.Errorf("%s: events = %+v, want %+v", name, events, want)
		}
	}
}

func TestRoomPresence(t *testing.T) {
	events := servePresence(t, `<presence xmlns="jabber:client" from="room@muc.example.test/alice" to="alice@example.test/laptop">
		<x xmlns="http://jabber.org/protocol/muc#user">
			<item affiliation="owner" role="moderator" jid="alice@example.test/laptop"/>
			<status code="110"/><status code="201"/>
		</x>
	</presence>`)
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	ev, ok := events[0].(RoomPresenceEvent)
	if !ok {
		t.Fatalf("event = %T", events[0])
	}
	if ev.Room != "room@muc.example.test" || ev.Nick != "alice" || ev.RealJID != "alice@example.test" ||
		ev.Affiliation != "owner" || ev.Role != "moderator" || !ev.Available ||
		!ev.Has(MUCStatusSelf) || !ev.Has(MUCStatusCreated) {
		t.Errorf("event = %+v", ev)
	}

	events = servePresence(t, `<presence xmlns="jabber:client" from="room@muc.example.test/bob" type="unavailable">
		<x xmlns="http://jabber.org/protocol/muc#user"><item affiliation="none" role="none" nick="robert"/><status code="303"/></x>
	</presence>`)
	ev, ok = events[0].(RoomPresenceEvent)
	if !ok || ev.Available || ev.NewNick != "robert" || !ev.Has(MUCStatusNickChanged) || ev.RealJID != "" {
		t.Errorf("nick change = %+v", events[0])
	}
}

func TestPresenceError(t *testing.T) {
	events := servePresence(t, `<presence xmlns="jabber:client" from="room@muc.example.test/alice" type="error">
		<x xmlns="http://jabber.org/protocol/muc"/>
		<error type="cancel"><conflict xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/></error>
	</presence>`)
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	ev, ok := events[0].(PresenceErrorEvent)
	if !ok || ev.From != "room@muc.example.test/alice" || ev.Condition != "conflict" {
		t.Errorf("event = %+v", events[0])
	}
}

func TestBookmarksPushOnlyFromOwnAccount(t *testing.T) {
	push := func(from string) string {
		return `<message xmlns="jabber:client" from="` + from + `" to="alice@example.test/laptop">
			<event xmlns="http://jabber.org/protocol/pubsub#event"><items node="urn:xmpp:bookmarks:1"><item id="room@muc.example.test"/></items></event>
		</message>`
	}
	if _, events := serveMessage(t, push("alice@example.test")); len(events) != 1 || events[0] != (BookmarksChangedEvent{}) {
		t.Errorf("own push: events = %+v", events)
	}
	if _, events := serveMessage(t, push("mallory@example.test")); len(events) != 0 {
		t.Errorf("someone else's push: events = %+v", events)
	}
}

// TestGroupchatSend checks a message to a room goes as groupchat, asking
// for no receipt and carrying no chat state, and a reply in it names the
// replied-to occupant.
func TestGroupchatSend(t *testing.T) {
	msg, err := newMessage("room@muc.example.test", "hi", SendOptions{
		Groupchat: true, ChatStateActive: true,
		ReplyToID: "g1", ReplyToJID: "room@muc.example.test/bob", QuotedAuthor: "bob", QuotedBody: "hi all",
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != stanza.GroupChatMessage || msg.Request != nil || msg.Active != nil {
		t.Errorf("message = %+v", msg)
	}
	if msg.Reply == nil || msg.Reply.To != "room@muc.example.test/bob" {
		t.Errorf("reply = %+v", msg.Reply)
	}
}

func TestOccupantJID(t *testing.T) {
	if err := ValidRoomNick("room@muc.example.test", "alice"); err != nil {
		t.Errorf("valid nick: %v", err)
	}
	if err := ValidRoomNick("room@muc.example.test", ""); err == nil {
		t.Error("empty nick accepted")
	}
}

// TestMUCFeaturesAdvertised pins what tells others we do rooms: XEP-0045
// §6.1, XEP-0249 §2 and the bookmarks pushes XEP-0402 §3.1 subscribes to.
func TestMUCFeaturesAdvertised(t *testing.T) {
	for _, want := range []string{"http://jabber.org/protocol/muc", "jabber:x:conference", "urn:xmpp:bookmarks:1+notify"} {
		if !slices.Contains(discoFeatures, want) {
			t.Errorf("discoFeatures is missing %q", want)
		}
	}
}

func TestOccupantID(t *testing.T) {
	events := servePresence(t, `<presence xmlns="jabber:client" from="room@muc.example.test/bob">
		<x xmlns="http://jabber.org/protocol/muc#user"><item affiliation="member" role="participant"/></x>
		<occupant-id xmlns="urn:xmpp:occupant-id:0" id="occ-bob"/>
	</presence>`)
	if ev, ok := events[0].(RoomPresenceEvent); !ok || ev.OccupantID != "occ-bob" {
		t.Errorf("presence = %+v", events[0])
	}
	_, events = serveMessage(t, `<message xmlns="jabber:client" from="room@muc.example.test/bob" type="groupchat" id="g1">
		<body>hi</body><occupant-id xmlns="urn:xmpp:occupant-id:0" id="occ-bob"/>
	</message>`)
	if msgs := messages(events); len(msgs) != 1 || msgs[0].OccupantID != "occ-bob" {
		t.Errorf("messages = %+v", msgs)
	}
	_, events = serveMessage(t, `<message xmlns="jabber:client" from="bob@example.test/pc" type="chat" id="c1">
		<body>hi</body><occupant-id xmlns="urn:xmpp:occupant-id:0" id="forged"/>
	</message>`)
	if msgs := messages(events); len(msgs) != 1 || msgs[0].OccupantID != "" {
		t.Errorf("an occupant-id outside a room taken: %+v", msgs)
	}
}

func TestRoomConfigChanged(t *testing.T) {
	_, events := serveMessage(t, `<message xmlns="jabber:client" from="room@muc.example.test" type="groupchat">
		<x xmlns="http://jabber.org/protocol/muc#user"><status code="104"/></x>
	</message>`)
	if len(events) != 1 || events[0] != (RoomConfigChangedEvent{Room: "room@muc.example.test"}) {
		t.Errorf("events = %+v", events)
	}
}

// TestMUCPrivateSend checks a private message to an occupant goes as a
// chat message to their occupant JID, marked as one from a room.
func TestMUCPrivateSend(t *testing.T) {
	msg, err := newMessage("room@muc.example.test/bob", "psst", SendOptions{MUCPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := xml.NewEncoder(&out).Encode(msg); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{`to="room@muc.example.test/bob"`, `type="chat"`, `<x xmlns="http://jabber.org/protocol/muc#user"></x>`} {
		if !strings.Contains(got, want) {
			t.Errorf("stanza %s lacks %s", got, want)
		}
	}
}

// TestMUCPrivateCarbon checks a private message our other device sent is
// marked private too, not taken for one to a contact.
func TestMUCPrivateCarbon(t *testing.T) {
	_, events := serveMessage(t, `<message xmlns="jabber:client" from="alice@example.test" to="alice@example.test/laptop">
		<sent xmlns="urn:xmpp:carbons:2"><forwarded xmlns="urn:xmpp:forward:0">
			<message xmlns="jabber:client" from="alice@example.test/phone" to="room@muc.example.test/bob" type="chat" id="p1">
				<body>psst</body><x xmlns="http://jabber.org/protocol/muc#user"/>
			</message>
		</forwarded></sent>
	</message>`)
	if msgs := messages(events); len(msgs) != 1 || !msgs[0].MUCPrivate || !msgs[0].Outgoing || msgs[0].To != "room@muc.example.test/bob" {
		t.Errorf("messages = %+v", msgs)
	}
}
