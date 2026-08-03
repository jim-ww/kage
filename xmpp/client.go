// Package xmpp wraps mellium.im/xmpp into a per-account client: dial/auth,
// roster fetch, sending messages, and listening for incoming stanzas. It
// moves plain stanza bodies only — encryption is layered on top by callers.
package xmpp

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mellium.im/sasl"
	"mellium.im/xmlstream"
	"mellium.im/xmpp"
	"mellium.im/xmpp/disco"
	"mellium.im/xmpp/disco/items"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/roster"
	"mellium.im/xmpp/stanza"
	"mellium.im/xmpp/upload"
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

// messageBody is a <message/> stanza carrying a <body/> and the optional
// XEP-0308 (correction) / XEP-0461 (reply) elements, used both for sending
// and for decoding incoming stanzas.
type messageBody struct {
	stanza.Message
	Body      string         `xml:"body,omitempty"`
	Replace   *replaceElem   `xml:"urn:xmpp:message-correct:0 replace"`
	Reply     *replyElem     `xml:"urn:xmpp:reply:0 reply"`
	Retract   *retractElem   `xml:"urn:xmpp:message-retract:1 retract"`
	Reactions *reactionsElem `xml:"urn:xmpp:reactions:0 reactions"`
	Fallback  *fallbackElem  `xml:"urn:xmpp:fallback:0 fallback"`

	// XEP-0085 chat state notification: at most one of these is set, on
	// send or receive. Modeled as five separate pointer fields (rather than
	// a single element with a variant name) because encoding/xml matches
	// struct fields to specific, fixed element names — there's no
	// "one-of-these-five-names" tag.
	Active    *struct{} `xml:"http://jabber.org/protocol/chatstates active"`
	Composing *struct{} `xml:"http://jabber.org/protocol/chatstates composing"`
	Paused    *struct{} `xml:"http://jabber.org/protocol/chatstates paused"`
	Inactive  *struct{} `xml:"http://jabber.org/protocol/chatstates inactive"`
	Gone      *struct{} `xml:"http://jabber.org/protocol/chatstates gone"`
}

// ChatState is a XEP-0085 chat state notification value.
type ChatState int

const (
	ChatStateActive ChatState = iota
	ChatStateComposing
	ChatStatePaused
	ChatStateInactive
	ChatStateGone
)

// chatState reports the chat state carried by msg, if any.
func (m messageBody) chatState() (ChatState, bool) {
	switch {
	case m.Composing != nil:
		return ChatStateComposing, true
	case m.Paused != nil:
		return ChatStatePaused, true
	case m.Inactive != nil:
		return ChatStateInactive, true
	case m.Gone != nil:
		return ChatStateGone, true
	case m.Active != nil:
		return ChatStateActive, true
	default:
		return ChatStateActive, false
	}
}

// setChatState sets the one pointer field on msg corresponding to state.
func (msg *messageBody) setChatState(state ChatState) {
	switch state {
	case ChatStateComposing:
		msg.Composing = &struct{}{}
	case ChatStatePaused:
		msg.Paused = &struct{}{}
	case ChatStateInactive:
		msg.Inactive = &struct{}{}
	case ChatStateGone:
		msg.Gone = &struct{}{}
	default:
		msg.Active = &struct{}{}
	}
}

// reactionsElem is XEP-0444: the complete, current set of reaction emoji
// this sender has applied to the message with this ID. A new <reactions/>
// stanza always fully replaces the sender's previous set — it is never a
// delta — including an empty Reactions slice to mean "I've cleared mine".
type reactionsElem struct {
	ID        string   `xml:"id,attr"`
	Reactions []string `xml:"urn:xmpp:reactions:0 reaction"`
}

// retractElem is XEP-0424: this message retracts an earlier one with this ID.
type retractElem struct {
	ID string `xml:"id,attr"`
}

// replaceElem is XEP-0308: this message corrects an earlier one with this ID.
type replaceElem struct {
	ID string `xml:"id,attr"`
}

// replyElem is XEP-0461: this message is a reply to the message with this ID,
// sent to/from To (mirrors the enclosing message's "to", the only JID that
// makes sense in a 1:1 conversation).
type replyElem struct {
	To string `xml:"to,attr"`
	ID string `xml:"id,attr"`
}

// fallbackElem declares which part of the body is fallback-only compatibility
// text (XEP-0461 for replies, XEP-0424 for retractions), so fallback-aware
// clients know what to strip/ignore. Body is a pointer so a bodyless
// <body/> (Start/End both nil — the whole message body is fallback, used for
// retractions) round-trips distinctly from omitting the element entirely.
type fallbackElem struct {
	For  string            `xml:"for,attr"`
	Body *fallbackBodyElem `xml:"urn:xmpp:fallback:0 body"`
}

// Start/End are pointers because XEP-0461 treats them as optional, each
// defaulting to "start of body"/"end of body" when absent — not to zero.
type fallbackBodyElem struct {
	Start *int `xml:"start,attr,omitempty"`
	End   *int `xml:"end,attr,omitempty"`
}

// presenceBody is a <presence/> stanza carrying an optional <show/>.
type presenceBody struct {
	stanza.Presence
	Show string `xml:"show"`
}

// SendOptions carries optional XEP-0308/XEP-0461 wire metadata for Send.
type SendOptions struct {
	// ReplaceID, if set, marks this message as a correction (XEP-0308) of
	// the earlier message with this ID.
	ReplaceID string

	// ReplyToID, if set, marks this message as a reply (XEP-0461) to the
	// message with this ID. QuotedAuthor/QuotedBody build the quoted-text
	// fallback for clients that don't understand XEP-0461.
	ReplyToID    string
	QuotedAuthor string
	QuotedBody   string

	// RetractID, if set, sends a XEP-0424 retraction of the earlier message
	// with this ID instead of a normal message. Mutually exclusive with the
	// other options above.
	RetractID string

	// ReactionTargetID, if set, sends a XEP-0444 reaction-set update instead
	// of a normal message: Reactions becomes the sender's complete, current
	// reaction set on the message with this ID (an empty slice clears it).
	// Mutually exclusive with the other options above.
	ReactionTargetID string
	Reactions        []string
}

const retractFallbackBody = "This person attempted to retract a previous message, but it's unsupported by your client."

// buildFallbackQuote renders XEP-0461's suggested quoted-text fallback:
// each line of the quoted message prefixed with "> ", first line labeled
// with the author, ending in a newline so it reads naturally before the
// real reply text that follows it in the body.
func buildFallbackQuote(author, body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) > 0 {
		lines[0] = author + ": " + lines[0]
	}
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// randomID generates a random stanza ID (128 bits, hex-encoded).
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing means the system RNG is broken; a predictable
		// fallback is still better than crashing message sends over it.
		return fmt.Sprintf("kage-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Send sends a chat-message stanza with the given body to "to", returning
// the stanza ID it was sent with (needed to later correct or be replied to).
func (c *Client) Send(ctx context.Context, to, body string, opts SendOptions) (string, error) {
	toJID, err := jid.Parse(to)
	if err != nil {
		return "", fmt.Errorf("parsing recipient %q: %w", to, err)
	}
	id := randomID()
	msg := messageBody{
		Message: stanza.Message{
			To:   toJID,
			Type: stanza.ChatMessage,
			ID:   id,
		},
		Body: body,
	}
	switch {
	case opts.ReactionTargetID != "":
		msg.Reactions = &reactionsElem{ID: opts.ReactionTargetID, Reactions: opts.Reactions}
	case opts.RetractID != "":
		msg.Retract = &retractElem{ID: opts.RetractID}
		msg.Body = retractFallbackBody
		msg.Fallback = &fallbackElem{
			For:  "urn:xmpp:message-retract:1",
			Body: &fallbackBodyElem{}, // no start/end: the whole body is fallback text
		}
	case opts.ReplaceID != "":
		msg.Replace = &replaceElem{ID: opts.ReplaceID}
	case opts.ReplyToID != "":
		quote := buildFallbackQuote(opts.QuotedAuthor, opts.QuotedBody)
		end := len(quote)
		msg.Body = quote + body
		msg.Reply = &replyElem{To: to, ID: opts.ReplyToID}
		msg.Fallback = &fallbackElem{
			For:  "urn:xmpp:reply:0",
			Body: &fallbackBodyElem{End: &end},
		}
	}
	if err := c.session.Encode(ctx, msg); err != nil {
		return "", err
	}
	return id, nil
}

// UploadFile uploads path using XEP-0363 HTTP File Upload and returns the
// service's download URL. The caller sends that URL as a normal message, which
// is both widely interoperable and lets recipients without attachment support
// still access the file.
func (c *Client) UploadFile(ctx context.Context, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("statting %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", path)
	}
	maxInt := int64(^uint(0) >> 1)
	if info.Size() > maxInt {
		return "", fmt.Errorf("%q is too large to upload", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening %q: %w", path, err)
	}
	defer f.Close()

	// Service discovery and slot negotiation are tiny XMPP round trips. Keep
	// them tightly bounded so a server that does not implement XEP-0363 cannot
	// leave the UI waiting forever before the HTTP transfer even begins.
	discoveryCtx, cancelDiscovery := context.WithTimeout(ctx, 15*time.Second)
	defer cancelDiscovery()
	service, err := c.uploadService(discoveryCtx)
	if err != nil {
		return "", err
	}
	contentType := mime.TypeByExtension(filepath.Ext(path))
	slot, err := upload.GetSlot(discoveryCtx, upload.File{
		Name: filepath.Base(path),
		Size: int(info.Size()),
		Type: contentType,
	}, service, c.session)
	if err != nil {
		return "", fmt.Errorf("requesting upload slot: %w", err)
	}
	if slot.PutURL == nil || slot.GetURL == nil {
		return "", fmt.Errorf("upload service returned an incomplete slot")
	}
	req, err := slot.Put(ctx, f)
	if err != nil {
		return "", fmt.Errorf("creating upload request: %w", err)
	}
	if contentType != "" {
		// slot.Put builds req.Header from slot.Header.Clone(); if the upload
		// service's slot response had no <header/> elements (the common case
		// — most services only send Authorization/Cookie when required),
		// slot.Header is nil and Clone() returns nil too, so req.Header must
		// be initialized before Set is called on it or this panics.
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		req.Header.Set("Content-Type", contentType)
	}
	// NewRequest cannot infer a length from an *os.File. Supplying it avoids
	// chunked transfer encoding, which a number of XEP-0363 services reject or
	// wait on indefinitely.
	req.ContentLength = info.Size()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("uploading file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("upload failed: HTTP %d", resp.StatusCode)
	}
	return slot.GetURL.String(), nil
}

// uploadService returns the JID of the account domain's XEP-0363 HTTP upload
// component, discovered via a disco walk the first time this is called and
// cached on c afterward — including a cached failure, so a server that just
// doesn't offer upload isn't re-walked on every single file send.
func (c *Client) uploadService(ctx context.Context) (jid.JID, error) {
	c.mu.Lock()
	if c.uploadSvcSet {
		svc, err := c.uploadSvc, c.uploadSvcErr
		c.mu.Unlock()
		return svc, err
	}
	c.mu.Unlock()

	svc, err := c.discoverUploadService(ctx)

	c.mu.Lock()
	c.uploadSvc, c.uploadSvcErr, c.uploadSvcSet = svc, err, true
	c.mu.Unlock()

	return svc, err
}

func (c *Client) discoverUploadService(ctx context.Context) (jid.JID, error) {
	root := c.JID.Domain()
	// XEP-0030 advertises HTTP-upload components as items of the account's
	// domain. Query items first: asking the domain for its own info before
	// this is unnecessary and some otherwise-working servers don't answer
	// that query promptly, making attachment sends appear stuck.
	iter := disco.FetchItems(ctx, items.Item{JID: root}, c.session)
	var services []items.Item
	for iter.Next() {
		services = append(services, iter.Item())
	}
	if err := iter.Err(); err != nil {
		_ = iter.Close()
		return jid.JID{}, fmt.Errorf("discovering upload service: %w", err)
	}
	// A disco item iterator holds the session response open. Mellium requires
	// it to be closed before starting another IQ request on that session.
	if err := iter.Close(); err != nil {
		return jid.JID{}, fmt.Errorf("closing upload-service discovery: %w", err)
	}
	for _, item := range services {
		info, err := disco.GetInfo(ctx, item.Node, item.JID, c.session)
		if err == nil && supportsUpload(info) {
			return item.JID, nil
		}
	}
	return jid.JID{}, fmt.Errorf("no XEP-0363 HTTP upload service advertised by %s", root)
}

func supportsUpload(info disco.Info) bool {
	for _, feature := range info.Features {
		if feature.Var == upload.NS {
			return true
		}
	}
	return false
}

// SendChatState sends a standalone XEP-0085 chat state notification to "to"
// — no body, just the state. Typically sent as the user starts typing
// (ChatStateComposing) and again once they stop without sending
// (ChatStateActive) or send it another way.
func (c *Client) SendChatState(ctx context.Context, to string, state ChatState) error {
	toJID, err := jid.Parse(to)
	if err != nil {
		return fmt.Errorf("parsing recipient %q: %w", to, err)
	}
	msg := messageBody{Message: stanza.Message{To: toJID, Type: stanza.ChatMessage, ID: randomID()}}
	msg.setChatState(state)
	return c.session.Encode(ctx, msg)
}

// Event is a value received from a Client's event stream.
type Event interface{ isEvent() }

// MessageEvent is an incoming chat message.
type MessageEvent struct {
	ID     string
	From   string
	Body   string // fallback-quote already stripped if this is a XEP-0461 reply
	SentAt time.Time

	// ReplaceID is non-empty if this is a XEP-0308 correction of an earlier
	// message with this ID.
	ReplaceID string

	// ReplyToID is non-empty if this is a XEP-0461 reply to the message with
	// this ID.
	ReplyToID string

	// RetractID is non-empty if this is a XEP-0424 retraction of an earlier
	// message with this ID. When set, the other fields besides ID/From/SentAt
	// carry no meaningful content (Body is just the compatibility fallback
	// text, if present at all).
	RetractID string

	// ReactionTargetID is non-empty if this is a XEP-0444 reaction-set update:
	// Reactions is the sender's complete, current reaction set on the
	// message with this ID (may be empty, meaning they cleared it). When
	// set, other fields besides ID/From/SentAt carry no meaningful content.
	ReactionTargetID string
	Reactions        []string
}

func (MessageEvent) isEvent() {}

// PresenceEvent is an incoming presence update.
type PresenceEvent struct {
	From string
	// Available is false for unavailable/error/subscription-management
	// presence (RFC 6121 §4.7.1) — anything other than a plain "here I am"
	// broadcast.
	Available bool
	// Show is the optional <show/> value when Available is true: "" (plain
	// online), "chat", "away", "xa", or "dnd".
	Show string
}

func (PresenceEvent) isEvent() {}

// ChatStateEvent is an incoming XEP-0085 chat state notification, standalone
// or attached to a regular message.
type ChatStateEvent struct {
	From  string
	State ChatState
}

func (ChatStateEvent) isEvent() {}

// Events returns the channel of incoming events, populated for the lifetime
// of the connection (from Dial until Close). The channel closes once the
// session ends.
func (c *Client) Events() <-chan Event {
	return c.events
}

func (c *Client) handleStanza(t xmlstream.TokenReadEncoder, start *xml.StartElement) {
	events := c.events
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

		if state, ok := msg.chatState(); ok {
			events <- ChatStateEvent{From: msg.From.String(), State: state}
		}

		if msg.Retract != nil {
			events <- MessageEvent{
				ID:        msg.ID,
				From:      msg.From.String(),
				SentAt:    time.Now(),
				RetractID: msg.Retract.ID,
			}
			return
		}

		if msg.Reactions != nil {
			reactions := msg.Reactions.Reactions
			if reactions == nil {
				reactions = []string{} // distinguish "cleared" from "field absent" for callers
			}
			events <- MessageEvent{
				ID:               msg.ID,
				From:             msg.From.String(),
				SentAt:           time.Now(),
				ReactionTargetID: msg.Reactions.ID,
				Reactions:        reactions,
			}
			return
		}

		if msg.Body == "" {
			return
		}

		body := msg.Body
		var replyToID string
		if msg.Reply != nil {
			replyToID = msg.Reply.ID
			if msg.Fallback != nil && msg.Fallback.For == "urn:xmpp:reply:0" && msg.Fallback.Body != nil {
				start := 0
				if msg.Fallback.Body.Start != nil {
					start = *msg.Fallback.Body.Start
				}
				end := len(body)
				if msg.Fallback.Body.End != nil {
					end = *msg.Fallback.Body.End
				}
				if start >= 0 && start <= end && end <= len(body) {
					body = body[:start] + body[end:]
				}
			}
		}
		var replaceID string
		if msg.Replace != nil {
			replaceID = msg.Replace.ID
		}

		events <- MessageEvent{
			ID:        msg.ID,
			From:      msg.From.String(),
			Body:      body,
			SentAt:    time.Now(),
			ReplaceID: replaceID,
			ReplyToID: replyToID,
		}
	case "presence":
		var p presenceBody
		d := xml.NewTokenDecoder(t)
		_ = d.DecodeElement(&p, start)
		events <- PresenceEvent{
			From:      p.From.String(),
			Available: p.Type == stanza.AvailablePresence,
			Show:      p.Show,
		}
	}
}
