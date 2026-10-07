package xmpp

import (
	"encoding/xml"
	"log/slog"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/roster"
	"mellium.im/xmpp/stanza"
)

// rosterQueryName is the roster-push payload mux dispatches on.
var rosterQueryName = xml.Name{Space: roster.NS, Local: "query"}

// rosterPushItem is the <item/> of a roster push. mellium's roster.Item is
// almost this, but drops the ask attribute - and "a request we sent is
// still pending" is exactly the state a client needs to tell apart from
// "not subscribed", so the element is decoded here instead.
type rosterPushItem struct {
	JID          string   `xml:"jid,attr"`
	Name         string   `xml:"name,attr"`
	Subscription string   `xml:"subscription,attr"`
	Ask          string   `xml:"ask,attr"`
	Groups       []string `xml:"group"`
}

type rosterPushQuery struct {
	Items []rosterPushItem `xml:"item"`
}

// handleRosterPush turns an RFC 6121 §2.1.6 roster push into a
// RosterPushEvent per item and acknowledges the IQ.
//
// A push whose from attribute is anything other than our own bare JID is
// dropped without a reply, per §2.1.6: honoring it would let any entity on
// the network rewrite our roster.
func (c *Client) handleRosterPush(iq stanza.IQ, t xmlstream.TokenReadEncoder, start *xml.StartElement) error {
	if !iq.From.Equal(jid.JID{}) && !iq.From.Equal(c.session.LocalAddr().Bare()) {
		slog.Warn("ignoring roster push from unauthorized sender", "from", iq.From.String())
		return nil
	}

	var q rosterPushQuery
	// Same decode caveat as handleStanza's message path: the stream is
	// already positioned inside start, so DecodeElement reports the closing
	// tag as an "unexpected end element" even on a fully successful decode.
	_ = xml.NewTokenDecoder(t).DecodeElement(&q, start)

	if _, err := xmlstream.Copy(t, iq.Result(nil)); err != nil {
		slog.Warn("acknowledging roster push", "err", err)
	}

	for _, item := range q.Items {
		if item.JID == "" {
			continue
		}
		j, err := jid.Parse(item.JID)
		if err != nil {
			slog.Warn("roster push: unparseable jid", "jid", item.JID, "err", err)
			continue
		}
		c.enqueue(RosterPushEvent{
			JID:          j.Bare().String(),
			Name:         item.Name,
			Subscription: item.Subscription,
			Ask:          item.Ask == "subscribe",
			Removed:      item.Subscription == "remove",
			Groups:       item.Groups,
		})
	}
	return nil
}
