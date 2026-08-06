package ipc

import (
	"encoding/json"
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

type echoParams struct{ Text string }
type echoResult struct{ Text string }

func echoHandler(method string, params json.RawMessage) (any, error) {
	var p echoParams
	json.Unmarshal(params, &p)
	return echoResult{Text: p.Text}, nil
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

	var result echoResult
	if err := conn.Call("Echo", echoParams{Text: "hello"}, &result); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.Text != "hello" {
		t.Fatalf("got %q, want %q", result.Text, "hello")
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
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var result echoResult
			text := string(rune('a' + i%26))
			if err := conn.Call("Echo", echoParams{Text: text}, &result); err != nil {
				failures.Add(1)
				return
			}
			if result.Text != text {
				failures.Add(1)
			}
		}(i)
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
	events := make([]chan Event, n)
	conns := make([]*Conn, n)
	for i := 0; i < n; i++ {
		ch := make(chan Event, 1)
		events[i] = ch
		c, err := Dial(sock, func(ev Event) { ch <- ev })
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		conns[i] = c
	}

	// give the server a moment to register all accepted connections before
	// broadcasting, since Accept's registration races with Dial returning.
	time.Sleep(50 * time.Millisecond)
	srv.Broadcast(Event{Kind: "Ping"})

	for i, ch := range events {
		select {
		case ev := <-ch:
			if ev.Kind != "Ping" {
				t.Fatalf("client %d: got kind %q", i, ev.Kind)
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
	go srv.Accept(ln, func(method string, params json.RawMessage) (any, error) {
		time.Sleep(200 * time.Millisecond)
		return echoResult{}, nil
	})

	conn, err := Dial(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	done := make(chan error, 1)
	go func() {
		var r echoResult
		done <- conn.Call("Echo", echoParams{Text: "x"}, &r)
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
