package xmpp

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/stanza"
)

const timeNS = "urn:xmpp:time"

// clockQueryTimeout bounds asking the server for its time while connecting.
const clockQueryTimeout = 5 * time.Second

// Now is the current time by the server's clock, as far as measuring it on
// connect could tell; the local clock if it couldn't. The server stamps
// delayed and archived messages, so a time from here sorts with theirs even
// when the local clock is off.
func (c *Client) Now() time.Time {
	return time.Now().Add(time.Duration(c.clockOffset.Load()))
}

// measureClock asks the server for its time (XEP-0202) and records how far
// it is from ours for Now.
func (c *Client) measureClock(ctx context.Context) error {
	ctx, cancel := c.bound(ctx)
	defer cancel()
	var reply struct {
		UTC string `xml:"utc"`
	}
	asked := time.Now()
	query := xmlstream.Wrap(nil, xml.StartElement{Name: xml.Name{Space: timeNS, Local: "time"}})
	if err := c.session.UnmarshalIQElement(ctx, query, stanza.IQ{Type: stanza.GetIQ, To: c.JID.Domain()}, &reply); err != nil {
		return err
	}
	answered := time.Now()
	server, ok := parseStamp(reply.UTC)
	if !ok {
		return fmt.Errorf("server time %q unparseable", reply.UTC)
	}
	c.clockOffset.Store(int64(clockOffset(asked, answered, server, !strings.Contains(reply.UTC, "."))))
	return nil
}

// clockOffset is how far ahead of ours a server clock is that read server
// between asked and answered, 0 if that is within what the reading can
// tell: the reply's trip, and the second a stamp without fractions was cut
// down to.
func clockOffset(asked, answered, server time.Time, wholeSeconds bool) time.Duration {
	uncertainty := answered.Sub(asked) / 2
	if wholeSeconds {
		server = server.Add(time.Second / 2)
		uncertainty += time.Second / 2
	}
	offset := server.Sub(asked.Add(answered.Sub(asked) / 2))
	if offset.Abs() <= uncertainty {
		return 0
	}
	return offset
}
