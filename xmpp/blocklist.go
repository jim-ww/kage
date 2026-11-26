package xmpp

import (
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/blocklist"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/stanza"
)

// The XEP-0191 blocklist pushes mux dispatches on.
var (
	blockName   = xml.Name{Space: blocklist.NS, Local: "block"}
	unblockName = xml.Name{Space: blocklist.NS, Local: "unblock"}
)

// blockPush is the <block/> or <unblock/> payload of a blocklist push.
type blockPush struct {
	XMLName xml.Name
	Items   []struct {
		JID string `xml:"jid,attr"`
	} `xml:"item"`
}

// Blocklist is the JIDs our server blocks for us (XEP-0191). Asking for it
// also makes the server push this stream every later change to it.
func (c *Client) Blocklist(ctx context.Context) ([]string, error) {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	iter := blocklist.Fetch(ctx, c.session)
	defer iter.Close()
	var jids []string
	for iter.Next() {
		jids = append(jids, iter.JID().String())
	}
	return jids, iter.Err()
}

// Block adds addr to our server's blocklist: nothing from it reaches any
// of our devices, and it sees us offline.
func (c *Client) Block(ctx context.Context, addr string) error {
	return c.changeBlocklist(ctx, addr, true)
}

// Unblock takes addr off our server's blocklist.
func (c *Client) Unblock(ctx context.Context, addr string) error {
	return c.changeBlocklist(ctx, addr, false)
}

func (c *Client) changeBlocklist(ctx context.Context, addr string, block bool) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	j, err := jid.Parse(addr)
	if err != nil {
		return fmt.Errorf("parsing JID %q: %w", addr, err)
	}
	if block {
		return blocklist.Add(ctx, c.session, j.Bare())
	}
	return blocklist.Remove(ctx, c.session, j.Bare())
}

// handleBlockPush turns a XEP-0191 blocklist push into a BlocklistPushEvent
// and acknowledges it. As with roster pushes, one from anyone but our own
// account is dropped unanswered.
func (c *Client) handleBlockPush(iq stanza.IQ, t xmlstream.TokenReadEncoder, start *xml.StartElement) error {
	if !iq.From.Equal(jid.JID{}) && !iq.From.Equal(c.session.LocalAddr().Bare()) {
		slog.Warn("ignoring blocklist push from unauthorized sender", "from", iq.From.String())
		return nil
	}
	var p blockPush
	// See handleRosterPush: a successful decode still reports an error.
	_ = xml.NewTokenDecoder(t).DecodeElement(&p, start)
	if _, err := xmlstream.Copy(t, iq.Result(nil)); err != nil {
		slog.Warn("acknowledging blocklist push", "err", err)
	}
	c.enqueue(blocklistPushEvent(p))
	return nil
}

// blocklistPushEvent is the event a decoded push reports.
func blocklistPushEvent(p blockPush) BlocklistPushEvent {
	ev := BlocklistPushEvent{Blocked: p.XMLName.Local == blockName.Local}
	for _, item := range p.Items {
		j, err := jid.Parse(item.JID)
		if err != nil {
			slog.Warn("blocklist push: unparseable jid", "jid", item.JID, "err", err)
			continue
		}
		ev.JIDs = append(ev.JIDs, j.String())
	}
	return ev
}
