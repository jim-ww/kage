package xmpp

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"mellium.im/xmpp"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/stream"
)

func TestClockOffset(t *testing.T) {
	asked := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	answered := asked.Add(200 * time.Millisecond)
	read := asked.Add(100 * time.Millisecond) // halfway through the trip
	for _, tc := range []struct {
		name         string
		server       time.Time
		wholeSeconds bool
		want         time.Duration
	}{
		{"in step", read, false, 0},
		{"within the trip", read.Add(90 * time.Millisecond), false, 0},
		{"ahead", read.Add(3 * time.Minute), false, 3 * time.Minute},
		{"behind", read.Add(-90 * time.Second), false, -90 * time.Second},
		// A whole-second stamp is anywhere in its second: midway is taken.
		{"in step, whole seconds", read.Truncate(time.Second), true, 0},
		{"ahead, whole seconds", read.Add(time.Minute).Truncate(time.Second), true, time.Minute + 400*time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clockOffset(asked, answered, tc.server, tc.wholeSeconds); got != tc.want {
				t.Errorf("clockOffset = %v, want %v", got, tc.want)
			}
		})
	}
}

// timeServerClient is a Client whose server answers XEP-0202 time queries
// with its clock running ahead of ours.
func timeServerClient(t *testing.T, ahead time.Duration) *Client {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { clientConn.Close(); serverConn.Close() })
	go func() {
		dec := xml.NewDecoder(serverConn)
		for {
			tok, err := dec.Token()
			if err != nil {
				return
			}
			start, ok := tok.(xml.StartElement)
			if !ok || start.Name.Local != "iq" {
				continue
			}
			var id string
			for _, a := range start.Attr {
				if a.Name.Local == "id" {
					id = a.Value
				}
			}
			if err := dec.Skip(); err != nil {
				return
			}
			utc := time.Now().Add(ahead).UTC().Format("2006-01-02T15:04:05.000Z")
			_, _ = fmt.Fprintf(serverConn, `<iq xmlns="jabber:client" type="result" id="%s" from="example.test"><time xmlns="urn:xmpp:time"><tzo>+00:00</tzo><utc>%s</utc></time></iq>`, id, utc)
		}
	}()

	ready := func(context.Context, *stream.Info, *stream.Info, *xmpp.Session, interface{}) (xmpp.SessionState, io.ReadWriter, interface{}, error) {
		return xmpp.Ready, nil, nil, nil
	}
	j := jid.MustParse("alice@example.test/laptop")
	session, err := xmpp.NewSession(context.Background(), j.Domain(), j, clientConn, 0, ready)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{JID: j, session: session, events: make(chan Event, 32)}
	c.life, c.endLife = context.WithCancel(context.Background())
	c.evCond = sync.NewCond(&c.evMu)
	c.discoMux = newDiscoMux(c.handleJingleIQ, c.handleRosterPush, c.handleBlockPush)
	go c.serve()
	go c.forwardEvents()
	return c
}

// TestNowFollowsServerClock checks Now runs by the server's clock once
// measured, so what we date sorts with what the server stamps.
func TestNowFollowsServerClock(t *testing.T) {
	c := timeServerClient(t, 10*time.Minute)
	if d := c.Now().Sub(time.Now()).Abs(); d > time.Second {
		t.Errorf("before measuring, Now is %v off the local clock", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.measureClock(ctx); err != nil {
		t.Fatal(err)
	}
	if d := c.Now().Sub(time.Now().Add(10 * time.Minute)).Abs(); d > time.Second {
		t.Errorf("Now is %v off the server's clock", d)
	}
}

// TestLiveMessageDatedByServerClock checks an undelayed message is dated by
// the server's clock, and a delayed one by its stamp, encrypted or not.
func TestLiveMessageDatedByServerClock(t *testing.T) {
	const ahead = 10 * time.Minute
	omemo := `<encrypted xmlns="eu.siacs.conversations.axolotl"><header sid="1"><iv>AAAA</iv></header><payload>AAAA</payload></encrypted>`
	stamp := time.Date(2026, 11, 20, 13, 20, 5, 0, time.UTC)
	delay := `<delay xmlns="urn:xmpp:delay" from="example.test" stamp="2026-11-20T13:20:05Z"/>`
	for _, tc := range []struct {
		name, inner string
		delayed     bool
	}{
		{"plain", `<body>hi</body>`, false},
		{"omemo", omemo, false},
		{"plain, delayed", `<body>hi</body>` + delay, true},
		{"omemo, delayed", omemo + delay, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{JID: jid.MustParse("alice@example.test/laptop")}
			c.clockOffset.Store(int64(ahead))
			_, events := serveMessageTo(t, c, `<message xmlns="jabber:client" from="bob@example.test/pc" to="alice@example.test/laptop" type="chat" id="m1">`+tc.inner+`</message>`)
			msgs := messages(events)
			if len(msgs) != 1 {
				t.Fatalf("%d messages, want 1", len(msgs))
			}
			got := msgs[0].SentAt
			if tc.delayed {
				if !got.Equal(stamp) {
					t.Errorf("SentAt = %v, want the stamp %v", got, stamp)
				}
			} else if d := got.Sub(time.Now().Add(ahead)).Abs(); d > time.Second {
				t.Errorf("SentAt is %v off the server's clock", d)
			}
		})
	}
}
