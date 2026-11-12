package ipc

import (
	"bufio"
	"encoding/gob"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"sync"
	"time"
)

// writeTimeout bounds every write to a client connection: a client whose
// reader has stalled (e.g. its own process wedged) never errors the write,
// net.Conn.Write just blocks forever.
const writeTimeout = 5 * time.Second

// ClientID identifies one attached client connection, unique for the life of
// the Server. It's passed to every Handler call and to OnDisconnect so the
// daemon can keep state per attached TUI (which chat it has open, whether
// it's left a chat state on the wire) and drop exactly that client's share of
// it when it goes away — several TUIs can be attached at once, so "the
// attached client" is never a safe assumption.
type ClientID uint64

// Handler answers one call from client. A non-nil error goes back
// stringified, alongside reply.
type Handler func(client ClientID, call any) (reply any, err error)

// Server accepts client connections on a Unix socket, dispatches incoming
// calls to a Handler, and lets the daemon Broadcast Events to every
// currently-connected client.
type Server struct {
	mu     sync.Mutex
	conns  map[*serverConn]struct{}
	nextID ClientID

	// OnDisconnect, if set, is called with the ClientID of every client
	// connection as it drops — so the daemon can undo state that only makes
	// sense while that particular TUI was attached (see background.go's
	// clientFocus and adapter.clearTyping).
	OnDisconnect func(client ClientID)
}

// serverConn is one accepted client connection; writeMu serializes replies
// and events, since both share the same gob stream.
type serverConn struct {
	id      ClientID
	nc      net.Conn
	writeMu sync.Mutex
	enc     *gob.Encoder
}

// write sends f within writeTimeout. Any failure closes the connection: a
// gob stream that failed mid-message (or whose encoder recorded a type it
// never managed to send) can't be written to again.
func (sc *serverConn) write(f frame) error {
	sc.writeMu.Lock()
	defer sc.writeMu.Unlock()
	sc.nc.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := sc.enc.Encode(f)
	sc.nc.SetWriteDeadline(time.Time{})
	if err != nil {
		slog.Error("ipc: writing to client, dropping it", "client", sc.id, "err", err)
		sc.nc.Close()
	}
	return err
}

// NewServer returns a new Server.
func NewServer() *Server {
	return &Server{conns: make(map[*serverConn]struct{})}
}

// Accept loops accepting connections from ln until it errors (typically
// because the listener was closed), serving each on its own goroutine via
// handler. It blocks the calling goroutine — callers run it in a `go`
// statement.
func (s *Server) Accept(ln net.Listener, handler Handler) error {
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.nextID++
		sc := &serverConn{id: s.nextID, nc: nc, enc: gob.NewEncoder(nc)}
		s.conns[sc] = struct{}{}
		s.mu.Unlock()
		go s.serve(sc, handler)
	}
}

// ClientCount reports how many clients are currently attached. Lets the
// daemon tell "nobody is looking at this" apart from "a TUI is attached but
// has another chat open" — see handleIncomingMessage's unread bookkeeping.
func (s *Server) ClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *Server) serve(sc *serverConn, handler Handler) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, sc)
		cb := s.OnDisconnect
		s.mu.Unlock()
		sc.nc.Close()
		if cb != nil {
			cb(sc.id)
		}
	}()

	dec := gob.NewDecoder(bufio.NewReader(sc.nc))
	for {
		var f frame
		if err := dec.Decode(&f); err != nil {
			return
		}
		go s.handle(sc, f, handler)
	}
}

func (s *Server) handle(sc *serverConn, call frame, handler Handler) {
	reply, err := callHandler(sc.id, call.Call, handler)
	f := frame{ID: call.ID, Reply: reply}
	if err != nil {
		// The requesting TUI shows the error; this is its only trace in the
		// log.
		slog.Warn("ipc: call failed", "call", callName(call.Call), "client", sc.id, "err", err)
		f.Err = err.Error()
	}
	sc.write(f)
}

// callHandler runs handler with a recover, so a panic deep in a single call
// logs a full stack trace and fails that one call instead of taking down
// the whole daemon - each call runs on its own goroutine (see serve), and an
// unrecovered panic in any goroutine kills the entire process.
func callHandler(client ClientID, call any, handler Handler) (reply any, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("ipc: panic handling call", "call", callName(call), "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("internal error handling %s: %v", callName(call), r)
		}
	}()
	return handler(client, call)
}

// Broadcast sends ev to every currently-connected client. Each connection is
// written to on its own goroutine with a bounded write deadline, so a client
// whose reader has stalled can't hold up delivery to every other one. A
// client that times out or errors is closed outright; serve's deferred
// cleanup then drops it from conns.
func (s *Server) Broadcast(ev any) {
	s.mu.Lock()
	conns := make([]*serverConn, 0, len(s.conns))
	for sc := range s.conns {
		conns = append(conns, sc)
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, sc := range conns {
		wg.Go(func() { sc.write(frame{Event: ev}) })
	}
	wg.Wait()
}

// Close closes every currently-connected client's connection.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sc := range s.conns {
		sc.nc.Close()
	}
}
