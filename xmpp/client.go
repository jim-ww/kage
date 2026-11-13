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
	"mellium.im/xmpp/disco"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/mux"
	"mellium.im/xmpp/ping"
	"mellium.im/xmpp/roster"
	"mellium.im/xmpp/stanza"
)

// Client is one account's connection. Its stream is read from Dial on, as
// IQ responses need it.
type Client struct {
	JID     jid.JID
	session *xmpp.Session
	events  chan Event

	closed atomic.Bool // set by Close; distinguishes intentional shutdown from a dropped connection

	// evMu/evCond/evBuf/evClosed buffer events without bound, so the stream
	// reader never blocks on a slow Events() reader: it also delivers our
	// IQ responses, so blocking it would wedge the connection.
	// forwardEvents feeds the channel.
	evMu     sync.Mutex
	evCond   *sync.Cond
	evBuf    []Event
	evClosed bool

	mu                    sync.Mutex
	err                   error   // set when serve() returns, e.g. on an unexpected disconnect
	uploadSvc             jid.JID // cached result of uploadService's disco walk, once found
	uploadSvcSet          bool    // true once uploadSvc has been resolved (even if disco found none — see uploadSvcErr)
	uploadSvcErr          error   // cached failure, so a server with no upload service doesn't get re-walked on every send
	invisibleSupported    bool    // cached result of InvisibleSupported's disco#info check
	invisibleSupportedSet bool    // true once invisibleSupported has been resolved by a successful disco round trip

	// discoMux answers incoming disco#info/disco#items queries (see disco.go)
	// - without it, contacts can't learn we support OMEMO at all.
	discoMux *mux.ServeMux

	// mamMu guards mamWaiters, the set of in-flight FetchArchive calls keyed
	// by their queryid — populated by handleStanza as MAM <result/> messages
	// stream in, ahead of the <iq> fin that FetchArchive is blocked on.
	mamMu      sync.Mutex
	mamWaiters map[string]chan ArchivedMessage
}

// Dial connects and authenticates address with password, sends initial
// presence and starts reading the stream. A nil tlsConfig verifies against
// the system roots.
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

	c := &Client{JID: j, session: session, events: make(chan Event, 32)}
	c.evCond = sync.NewCond(&c.evMu)
	c.discoMux = newDiscoMux(c.handleJingleIQ, c.handleRosterPush)
	go c.serve()
	go c.forwardEvents()

	// Caps (XEP-0115) in presence tell contacts which resource to ask about
	// features: the server answers disco#info to our bare JID itself.
	if err := session.Send(ctx, stanza.Presence{Type: stanza.AvailablePresence}.Wrap(discoCaps().TokenReader())); err != nil {
		c.Close()
		return nil, fmt.Errorf("sending initial presence: %w", err)
	}

	// Carbons (XEP-0280), so we get our other devices' messages too.
	// Best-effort.
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

// SetPresence sets our <show/> ("" for online), with our caps as Dial does.
func (c *Client) SetPresence(ctx context.Context, show string) error {
	// Logged here rather than at the call sites so every route to a status
	// change is covered by one line: the user toggling it, the initial
	// presence on connect, and the restore after a reconnect.
	slog.Debug("advertising own presence", "jid", c.JID.String(), "show", showOrOnline(show))
	children := []xml.TokenReader{discoCaps().TokenReader()}
	if show != "" {
		children = append(children, xmlstream.Wrap(
			xmlstream.Token(xml.CharData(show)),
			xml.StartElement{Name: xml.Name{Local: "show"}},
		))
	}
	return c.session.Send(ctx, stanza.Presence{Type: stanza.AvailablePresence}.Wrap(xmlstream.MultiReader(children...)))
}

// invisibleNS is the XEP-0186 disco#info feature var a server advertises on
// its own JID if it supports invisible presence.
const invisibleNS = "urn:xmpp:invisible:0"

// InvisibleSupported reports whether the server offers XEP-0186 invisible
// presence, caching the answer.
func (c *Client) InvisibleSupported(ctx context.Context) bool {
	c.mu.Lock()
	if c.invisibleSupportedSet {
		supported := c.invisibleSupported
		c.mu.Unlock()
		return supported
	}
	c.mu.Unlock()

	info, err := disco.GetInfo(ctx, "", c.JID.Domain(), c.session)
	if err != nil {
		// Cached as "no" too, so a silent server isn't waited on again; a
		// reconnect asks anew.
		slog.Debug("checking invisible presence support", "jid", c.JID.String(), "err", err)
		c.mu.Lock()
		c.invisibleSupported, c.invisibleSupportedSet = false, true
		c.mu.Unlock()
		return false
	}
	supported := false
	for _, f := range info.Features {
		if f.Var == invisibleNS {
			supported = true
			break
		}
	}

	c.mu.Lock()
	c.invisibleSupported, c.invisibleSupportedSet = supported, true
	c.mu.Unlock()
	return supported
}

// InvisibleSupportedCached is what InvisibleSupported knows, without a round
// trip; ok is false if not asked yet.
func (c *Client) InvisibleSupportedCached() (supported, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.invisibleSupported, c.invisibleSupportedSet
}

// SetInvisible sends XEP-0186 invisible presence: contacts see us offline.
// Only after InvisibleSupported.
func (c *Client) SetInvisible(ctx context.Context) error {
	slog.Debug("advertising invisible presence", "jid", c.JID.String())
	return c.session.Send(ctx, stanza.Presence{Type: stanza.PresenceType("invisible")}.Wrap(discoCaps().TokenReader()))
}

// showOrOnline renders an empty <show/> as "online" for logging, so a plain
// available presence doesn't read as a missing value.
func showOrOnline(show string) string {
	if show == "" {
		return "online"
	}
	return show
}

// serve reads the stream until it closes; IQ responses need it read.
func (c *Client) serve() {
	defer c.closeEventQueue()
	err := c.session.Serve(xmpp.HandlerFunc(func(t xmlstream.TokenReadEncoder, start *xml.StartElement) error {
		c.handleStanza(t, start)
		return nil
	}))
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

// enqueue buffers ev for forwardEvents. Never blocks, see Client.evBuf.
func (c *Client) enqueue(ev Event) {
	c.evMu.Lock()
	if c.evClosed {
		c.evMu.Unlock()
		return
	}
	c.evBuf = append(c.evBuf, ev)
	c.evMu.Unlock()
	c.evCond.Signal()
}

// closeEventQueue marks the queue closed; forwardEvents drains whatever's
// left and then closes c.events.
func (c *Client) closeEventQueue() {
	c.evMu.Lock()
	c.evClosed = true
	c.evMu.Unlock()
	c.evCond.Signal()
}

// forwardEvents is the only goroutine that ever sends on c.events, and so
// the only one that can ever block waiting for a slow or absent consumer -
// deliberately isolated from serve()'s stream-reading loop via evBuf.
func (c *Client) forwardEvents() {
	defer close(c.events)
	for {
		c.evMu.Lock()
		for len(c.evBuf) == 0 && !c.evClosed {
			c.evCond.Wait()
		}
		if len(c.evBuf) == 0 {
			c.evMu.Unlock()
			return
		}
		ev := c.evBuf[0]
		c.evBuf = c.evBuf[1:]
		c.evMu.Unlock()
		c.events <- ev
	}
}

// Close ends the session, closing Events, and marks it closed on purpose.
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

// Kill closes the connection like a drop would, not marked closed on purpose,
// for a connection known dead without a read error (half-open), so it's
// reconnected.
func (c *Client) Kill() {
	_ = c.session.Conn().Close()
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
	// Ask is true while a subscription request we sent is still waiting
	// for the contact's approval (the item's ask="subscribe").
	Ask bool
}

// Roster fetches the account's contact list from the server. Decoded here
// rather than through mellium's roster.Fetch for the same reason as roster
// pushes (see rosterPushItem): its Item drops the ask attribute.
func (c *Client) Roster(ctx context.Context) ([]Contact, error) {
	var q rosterPushQuery
	query := xmlstream.Wrap(nil, xml.StartElement{Name: rosterQueryName})
	if err := c.session.UnmarshalIQElement(ctx, query, stanza.IQ{Type: stanza.GetIQ}, &q); err != nil {
		return nil, fmt.Errorf("fetching roster: %w", err)
	}
	contacts := make([]Contact, 0, len(q.Items))
	for _, item := range q.Items {
		j, err := jid.Parse(item.JID)
		if err != nil || item.JID == "" {
			continue
		}
		contacts = append(contacts, Contact{
			JID:          j.String(),
			Name:         item.Name,
			Subscription: item.Subscription,
			Ask:          item.Ask == "subscribe",
		})
	}
	return contacts, nil
}

// SetRosterName sets addr's roster name, "" to clear it (RFC 6121).
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

// ProbePresence asks for addr's current presence, rather than relying on the
// server's initial burst alone.
func (c *Client) ProbePresence(ctx context.Context, addr string) error {
	j, err := jid.Parse(addr)
	if err != nil {
		return fmt.Errorf("parsing JID %q: %w", addr, err)
	}
	return c.session.Send(ctx, stanza.Presence{Type: stanza.ProbePresence, To: j.Bare()}.Wrap(nil))
}

// Ping pings our server (XEP-0199) and waits for its answer.
func (c *Client) Ping(ctx context.Context) error {
	return ping.Send(ctx, c.session, jid.JID{})
}

// DeviceName is the client name fullJID advertises (XEP-0030), "" if none.
func (c *Client) DeviceName(ctx context.Context, fullJID string) (string, error) {
	j, err := jid.Parse(fullJID)
	if err != nil {
		return "", fmt.Errorf("parsing JID %q: %w", fullJID, err)
	}
	info, err := disco.GetInfo(ctx, "", j, c.session)
	if err != nil {
		return "", err
	}
	for _, id := range info.Identity {
		if id.Category == "client" && id.Name != "" {
			return id.Name, nil
		}
	}
	return "", nil
}

// ResubscribeContact asks addr for their presence again, roster untouched.
func (c *Client) ResubscribeContact(ctx context.Context, addr string) error {
	j, err := jid.Parse(addr)
	if err != nil {
		return fmt.Errorf("parsing JID %q: %w", addr, err)
	}
	return c.session.Send(ctx, stanza.Presence{Type: stanza.SubscribePresence, To: j.Bare()}.Wrap(nil))
}

// RemoveContact removes addr from the roster and cancels both directions of
// the subscription, which the removal alone wouldn't.
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
