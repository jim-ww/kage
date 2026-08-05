// Package xmpp wraps mellium.im/xmpp into a per-account client: dial/auth,
// roster fetch, sending messages, and listening for incoming stanzas. It
// moves plain stanza bodies only — encryption is layered on top by callers.
package xmpp

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"mellium.im/sasl"
	"mellium.im/xmlstream"
	"mellium.im/xmpp"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/mux"
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

	closed atomic.Bool // set by Close; distinguishes intentional shutdown from a dropped connection

	mu           sync.Mutex
	err          error   // set when serve() returns, e.g. on an unexpected disconnect
	uploadSvc    jid.JID // cached result of uploadService's disco walk, once found
	uploadSvcSet bool    // true once uploadSvc has been resolved (even if disco found none — see uploadSvcErr)
	uploadSvcErr error   // cached failure, so a server with no upload service doesn't get re-walked on every send

	// discoMux answers incoming disco#info/disco#items queries (see disco.go)
	// - without it, contacts can't learn we support OMEMO at all.
	discoMux *mux.ServeMux

	// mamMu guards mamWaiters, the set of in-flight FetchArchive calls keyed
	// by their queryid — populated by handleStanza as MAM <result/> messages
	// stream in, ahead of the <iq> fin that FetchArchive is blocked on.
	mamMu      sync.Mutex
	mamWaiters map[string]chan ArchivedMessage
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
	session, err := xmpp.DialClientSession(
		ctx, j,
		xmpp.BindResource(),
		xmpp.StartTLS(tlsConfig),
		xmpp.SASL("", password, sasl.ScramSha1Plus, sasl.ScramSha1, sasl.Plain),
	)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", j, err)
	}

	c := &Client{JID: j, session: session, events: make(chan Event, 32), discoMux: newDiscoMux()}
	go c.serve()

	// Advertise our features via XEP-0115 entity capabilities on the initial
	// presence - without this, contacts have no signal that we support
	// anything (OMEMO included): disco#info to our bare JID is answered by
	// the server itself, not forwarded to us, so caps carried in presence are
	// what tells a contact's client which resource of ours to actually query.
	if err := session.Send(ctx, stanza.Presence{Type: stanza.AvailablePresence}.Wrap(discoCaps().TokenReader())); err != nil {
		c.Close()
		return nil, fmt.Errorf("sending initial presence: %w", err)
	}

	// XEP-0280: ask the server to carbon-copy messages sent/received by our
	// other resources to us too. Without this, a message addressed to our
	// bare JID is delivered to only one connected resource (server's choice,
	// commonly whichever is most recently active) - fine for a single
	// client, but it means a second resource on the same account (notifyd,
	// running alongside the TUI) can go "connected" and never see a single
	// message. Best-effort: an older server without carbons support just
	// means no benefit, not a failed connection.
	if err := c.enableCarbons(ctx); err != nil {
		slog.Warn("enabling message carbons", "err", err)
	}
	return c, nil
}

const carbonsNS = "urn:xmpp:carbons:2"

func (c *Client) enableCarbons(ctx context.Context) error {
	iq := stanza.IQ{Type: stanza.SetIQ, ID: randomID()}
	rc, err := c.session.SendIQElement(ctx, xmlstream.Wrap(nil, xml.StartElement{Name: xml.Name{Space: carbonsNS, Local: "enable"}}), iq)
	if err != nil {
		return err
	}
	return rc.Close()
}

// SetPresence updates our advertised availability: show is "" for plain
// online, or "away"/"xa"/"dnd" per RFC 6121 §4.7.2.1. Re-sends our XEP-0115
// caps too, same as Dial's initial presence, so a status change never drops
// the capabilities advertisement contacts rely on to discover OMEMO support.
func (c *Client) SetPresence(ctx context.Context, show string) error {
	children := []xml.TokenReader{discoCaps().TokenReader()}
	if show != "" {
		children = append(children, xmlstream.Wrap(
			xmlstream.Token(xml.CharData(show)),
			xml.StartElement{Name: xml.Name{Local: "show"}},
		))
	}
	return c.session.Send(ctx, stanza.Presence{Type: stanza.AvailablePresence}.Wrap(xmlstream.MultiReader(children...)))
}

// serve reads the session's stream until it closes, dispatching incoming
// stanzas to c.events. It must run for the entire lifetime of the session —
// SendIQ (used by Roster, and internally by presence/message delivery
// acknowledgement) blocks forever waiting for a response if nothing is
// reading the stream.
func (c *Client) serve() {
	defer close(c.events)
	err := c.session.Serve(xmpp.HandlerFunc(func(t xmlstream.TokenReadEncoder, start *xml.StartElement) error {
		c.handleStanza(t, start)
		return nil
	}))
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

// Close ends the session and its underlying connection. This unblocks and
// terminates the background serve loop, closing the Events channel. Marks
// the client as intentionally closed, so callers watching Events()/Err() can
// tell a deliberate shutdown apart from a dropped connection.
func (c *Client) Close() error {
	c.closed.Store(true)
	if err := c.session.Close(); err != nil {
		return err
	}
	return c.session.Conn().Close()
}

// Closed reports whether Close was called on this client (as opposed to the
// connection having dropped unexpectedly).
func (c *Client) Closed() bool {
	return c.closed.Load()
}

// Err returns the error that ended the background serve loop, if any. Only
// meaningful after the Events channel has closed.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
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

// SetRosterName updates addr's roster item to display as name (empty clears
// any custom nickname, falling back to the JID). The server applies this to
// the item's existing subscription/groups — only jid and name are sent, per
// RFC 6121 roster sets.
func (c *Client) SetRosterName(ctx context.Context, addr, name string) error {
	j, err := jid.Parse(addr)
	if err != nil {
		return fmt.Errorf("parsing JID %q: %w", addr, err)
	}
	return roster.Set(ctx, c.session, roster.Item{JID: j.Bare(), Name: name})
}

// AddContact adds addr to the roster and sends a subscription request, per
// the usual RFC 6121 add-a-contact flow: a roster set followed by
// presence type="subscribe" so the peer's server prompts them to approve.
func (c *Client) AddContact(ctx context.Context, addr, name string) error {
	j, err := jid.Parse(addr)
	if err != nil {
		return fmt.Errorf("parsing JID %q: %w", addr, err)
	}
	if err := roster.Set(ctx, c.session, roster.Item{JID: j.Bare(), Name: name}); err != nil {
		return fmt.Errorf("adding roster item: %w", err)
	}
	return c.session.Send(ctx, stanza.Presence{Type: stanza.SubscribePresence, To: j.Bare()}.Wrap(nil))
}

// ApproveSubscription responds to an inbound subscription request from addr,
// granting it permission to see our presence.
func (c *Client) ApproveSubscription(ctx context.Context, addr string) error {
	j, err := jid.Parse(addr)
	if err != nil {
		return fmt.Errorf("parsing JID %q: %w", addr, err)
	}
	return c.session.Send(ctx, stanza.Presence{Type: stanza.SubscribedPresence, To: j.Bare()}.Wrap(nil))
}

// RemoveContact removes addr from the roster and cancels both halves of the
// subscription: unsubscribe (we stop receiving addr's presence) and
// unsubscribed (addr stops receiving ours) — a roster delete alone leaves
// any existing subscription in place server-side.
func (c *Client) RemoveContact(ctx context.Context, addr string) error {
	j, err := jid.Parse(addr)
	if err != nil {
		return fmt.Errorf("parsing JID %q: %w", addr, err)
	}
	if err := c.session.Send(ctx, stanza.Presence{Type: stanza.UnsubscribePresence, To: j.Bare()}.Wrap(nil)); err != nil {
		return fmt.Errorf("sending unsubscribe: %w", err)
	}
	if err := c.session.Send(ctx, stanza.Presence{Type: stanza.UnsubscribedPresence, To: j.Bare()}.Wrap(nil)); err != nil {
		return fmt.Errorf("sending unsubscribed: %w", err)
	}
	return roster.Delete(ctx, c.session, j.Bare())
}
