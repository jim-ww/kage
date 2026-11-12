package ipc

import (
	"encoding/gob"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "kage.sock")
}

type echoCall struct{ Text string }
type echoReply struct{ Text string }
type ping struct{ N int }

func init() {
	gob.Register(echoCall{})
	gob.Register(echoReply{})
	gob.Register(ping{})
}

func echoHandler(_ ClientID, call any) (any, error) {
	return echoReply{Text: call.(echoCall).Text}, nil
}

func TestClientServerRoundTrip(t *testing.T) {
	sock := testSocket(t)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	srv := NewServer()
	go srv.Accept(ln, echoHandler)

	conn, err := Dial(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	reply, err := conn.Call(echoCall{Text: "hello"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := reply.(echoReply).Text; got != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

func TestClientConcurrentCallsCorrelateByID(t *testing.T) {
	sock := testSocket(t)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	srv := NewServer()
	go srv.Accept(ln, echoHandler)

	conn, err := Dial(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	const n = 50
	var wg sync.WaitGroup
	var failures atomic.Int64
	for i := range n {
		wg.Go(func() {
			text := string(rune('a' + i%26))
			reply, err := conn.Call(echoCall{Text: text})
			if err != nil || reply.(echoReply).Text != text {
				failures.Add(1)
			}
		})
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d/%d concurrent calls failed or mismatched", failures.Load(), n)
	}
}

func TestServerBroadcastReachesAllClients(t *testing.T) {
	sock := testSocket(t)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	srv := NewServer()
	go srv.Accept(ln, echoHandler)

	const n = 3
	events := make([]chan any, n)
	conns := make([]*Conn, n)
	for i := range n {
		ch := make(chan any, 1)
		events[i] = ch
		c, err := Dial(sock, func(ev any) { ch <- ev })
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		conns[i] = c
	}

	// give the server a moment to register all accepted connections before
	// broadcasting, since Accept's registration races with Dial returning.
	time.Sleep(50 * time.Millisecond)
	srv.Broadcast(ping{N: 7})

	for i, ch := range events {
		select {
		case ev := <-ch:
			if ev != (ping{N: 7}) {
				t.Fatalf("client %d: got %#v", i, ev)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("client %d: did not receive broadcast event", i)
		}
	}
}

func TestCallAfterDisconnectDoesNotHang(t *testing.T) {
	sock := testSocket(t)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer()
	go srv.Accept(ln, func(ClientID, any) (any, error) {
		time.Sleep(200 * time.Millisecond)
		return echoReply{}, nil
	})

	conn, err := Dial(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	done := make(chan error, 1)
	go func() {
		_, err := conn.Call(echoCall{Text: "x"})
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	ln.Close()
	srv.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after server shutdown mid-call")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Call did not return after disconnect — pending response channel leaked")
	}
}

func TestCallError(t *testing.T) {
	sock := testSocket(t)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	srv := NewServer()
	go srv.Accept(ln, func(ClientID, any) (any, error) { panic("boom") })

	conn, err := Dial(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Call(echoCall{}); err == nil {
		t.Fatal("a panicking handler's call succeeded")
	}
	// The connection survives a failed call.
	if _, err := conn.Call(echoCall{}); err == nil {
		t.Fatal("second call succeeded too")
	}
}
