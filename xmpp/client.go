// Package xmpp wraps mellium.im/xmpp into a per-account client: dial/auth,
// roster fetch, sending messages, and listening for incoming stanzas. It
// moves plain stanza bodies only — encryption is layered on top by callers.
package xmpp

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"time"

	"mellium.im/sasl"
	"mellium.im/xmlstream"
	"mellium.im/xmpp"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/roster"
	"mellium.im/xmpp/stanza"
)

// Client is a connected session for a single XMPP account. The session's
// stream is read continuously in the background from the moment Dial
// returns — mellium.im/xmpp requires this for IQ round-trips (roster
// fetches, etc.) to ever receive their response, not just for receiving
// messages.
type Client struct {
	JID     jid.JID
	session *xmpp.Session
	events  chan Event
}

// Dial connects and authenticates address (a full or bare JID) with password,
// binds a resource, sends initial presence so the server starts delivering
// messages, and starts reading the stream in the background. tlsConfig is
// optional (nil uses a default config that verifies the server's certificate
// against the system trust store); pass one with a custom RootCAs pool for
// self-hosted servers using a private CA.
func Dial(ctx context.Context, address, password string, tlsConfig *tls.Config) (*Client, error) {
	j, err := jid.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("parsing jid %q: %w", address, err)
	}

	if tlsConfig == nil {
		tlsConfig = &tls.Config{ServerName: j.Domain().String()}
	}
	session, err := xmpp.DialClientSession(ctx, j,
		xmpp.BindResource(),
		xmpp.StartTLS(tlsConfig),
		xmpp.SASL("", password, sasl.ScramSha1Plus, sasl.ScramSha1, sasl.Plain),
	)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", j, err)
	}

	c := &Client{JID: j, session: session, events: make(chan Event, 32)}
	go c.serve()

	if err := session.Send(ctx, stanza.Presence{Type: stanza.AvailablePresence}.Wrap(nil)); err != nil {
		c.Close()
		return nil, fmt.Errorf("sending initial presence: %w", err)
	}
	return c, nil
}

// serve reads the session's stream until it closes, dispatching incoming
// stanzas to c.events. It must run for the entire lifetime of the session —
// SendIQ (used by Roster, and internally by presence/message delivery
// acknowledgement) blocks forever waiting for a response if nothing is
// reading the stream.
func (c *Client) serve() {
	defer close(c.events)
	c.session.Serve(xmpp.HandlerFunc(func(t xmlstream.TokenReadEncoder, start *xml.StartElement) error {
		handleStanza(c.events, t, start)
		return nil
	}))
}

// Close ends the session and its underlying connection. This unblocks and
// terminates the background serve loop, closing the Events channel.
func (c *Client) Close() error {
	if err := c.session.Close(); err != nil {
		return err
	}
	return c.session.Conn().Close()
}

// Contact is a single roster entry.
type Contact struct {
	JID          string
	Name         string
	Subscription string
}

// Roster fetches the account's contact list from the server.
func (c *Client) Roster(ctx context.Context) ([]Contact, error) {
	iter := roster.Fetch(ctx, c.session)
	defer iter.Close()

	var contacts []Contact
	for iter.Next() {
		item := iter.Item()
		contacts = append(contacts, Contact{
			JID:          item.JID.String(),
			Name:         item.Name,
			Subscription: item.Subscription,
		})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("fetching roster: %w", err)
	}
	return contacts, nil
}

// messageBody is a <message/> stanza carrying a <body/>, used both for
// sending and for decoding incoming stanzas.
type messageBody struct {
	stanza.Message
	Body string `xml:"body"`
}

// Send sends a chat-message stanza with the given body to "to".
func (c *Client) Send(ctx context.Context, to, body string) error {
	toJID, err := jid.Parse(to)
	if err != nil {
		return fmt.Errorf("parsing recipient %q: %w", to, err)
	}
	msg := messageBody{
		Message: stanza.Message{
			To:   toJID,
			Type: stanza.ChatMessage,
		},
		Body: body,
	}
	return c.session.Encode(ctx, msg)
}

// Event is a value received from a Client's event stream.
type Event interface{ isEvent() }

// MessageEvent is an incoming chat message.
type MessageEvent struct {
	From   string
	Body   string
	SentAt time.Time
}

func (MessageEvent) isEvent() {}

// PresenceEvent is an incoming presence update.
type PresenceEvent struct {
	From   string
	Status string
}

func (PresenceEvent) isEvent() {}

// Events returns the channel of incoming events, populated for the lifetime
// of the connection (from Dial until Close). The channel closes once the
// session ends.
func (c *Client) Events() <-chan Event {
	return c.events
}

func handleStanza(events chan<- Event, t xmlstream.TokenReadEncoder, start *xml.StartElement) {
	switch start.Name.Local {
	case "message":
		d := xml.NewTokenDecoder(t)
		var msg messageBody
		// DecodeElement legitimately returns a non-nil, non-io.EOF error here
		// (an "unexpected end element" for the message's own closing tag) even
		// on a fully successful decode, because the token stream handed to the
		// handler is already positioned inside the element start passed to us.
		// The decoded value is valid regardless; only bail if we got nothing.
		_ = d.DecodeElement(&msg, start)
		if msg.Body == "" {
			return
		}
		events <- MessageEvent{
			From:   msg.From.String(),
			Body:   msg.Body,
			SentAt: time.Now(),
		}
	case "presence":
		var p stanza.Presence
		d := xml.NewTokenDecoder(t)
		_ = d.DecodeElement(&p, start)
		events <- PresenceEvent{
			From:   p.From.String(),
			Status: string(p.Type),
		}
	}
}
