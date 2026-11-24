package xmpp

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"mellium.im/xmpp"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/stream"
)

// silentServerClient is a Client whose server reads everything and never
// answers, as over a half-open connection.
func silentServerClient(t *testing.T) *Client {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, serverConn) }()
	t.Cleanup(func() { clientConn.Close(); serverConn.Close() })

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
	c.discoMux = newDiscoMux(c.handleJingleIQ, c.handleRosterPush)
	go c.serve()
	go c.forwardEvents()
	return c
}

// TestRequestEndsWithStream checks a request waiting on an answer gives up
// when the connection dies, even under a context that never ends: the
// reconnect path asks under one, and waited on a dead connection forever.
func TestRequestEndsWithStream(t *testing.T) {
	c := silentServerClient(t)

	done := make(chan error, 1)
	go func() {
		_, err := c.Roster(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("Roster returned %v before any answer or disconnect", err)
	case <-time.After(200 * time.Millisecond):
	}

	c.Kill()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Roster succeeded on a dead connection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Roster still waiting after the connection died")
	}
}
