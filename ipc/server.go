package ipc

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"sync"
	"time"
)

// writeTimeout bounds every write to a client connection. Without it, a
// client whose reader has stalled (e.g. its own process wedged) never errors
// the write - net.Conn.Write just blocks forever - and since Broadcast used
// to write to every connection sequentially on one goroutine, one stuck
// client silently starved every other connected client of all events
// indefinitely.
const writeTimeout = 5 * time.Second

// Handler answers one RPC call: method identifies which one, params is its
// raw JSON args. Returning a non-nil error sends it back as Response.Err
// (stringified); result is marshaled into Response.Result.
type Handler func(method string, params json.RawMessage) (result any, err error)

// Server accepts client connections on a Unix socket, dispatches incoming
// Requests to a Handler, and lets the daemon Broadcast Events to every
// currently-connected client.
type Server struct {
	mu    sync.Mutex
	conns map[*serverConn]struct{}

	// OnLastDisconnect, if set, is called whenever the connected-client count
	// drops to zero — e.g. so the daemon can reset state that only makes
	// sense while a TUI is attached (see background.go's tuiFocused).
	OnLastDisconnect func()
}

// serverConn is one accepted client connection; writeMu serializes
// Responses and Broadcast Events, since both share the same net.Conn.
type serverConn struct {
	nc      net.Conn
	writeMu sync.Mutex
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
		sc := &serverConn{nc: nc}
		s.mu.Lock()
		s.conns[sc] = struct{}{}
		s.mu.Unlock()
		go s.serve(sc, handler)
	}
}

func (s *Server) serve(sc *serverConn, handler Handler) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, sc)
		empty := len(s.conns) == 0
		cb := s.OnLastDisconnect
		s.mu.Unlock()
		sc.nc.Close()
		if empty && cb != nil {
			cb()
		}
	}()

	for {
		t, body, err := readFrame(sc.nc)
		if err != nil {
			return
		}
		if t != tagRequest {
			continue
		}
		var req Request
		if err := json.Unmarshal(body, &req); err != nil {
			continue
		}
		go s.handle(sc, req, handler)
	}
}

func (s *Server) handle(sc *serverConn, req Request, handler Handler) {
	result, err := s.callHandler(req, handler)
	resp := Response{ID: req.ID}
	if err != nil {
		resp.Err = err.Error()
	} else if result != nil {
		b, merr := json.Marshal(result)
		if merr != nil {
			resp.Err = fmt.Errorf("ipc: marshaling result of %s: %w", req.Method, merr).Error()
		} else {
			resp.Result = b
		}
	}

	sc.writeMu.Lock()
	sc.nc.SetWriteDeadline(time.Now().Add(writeTimeout))
	writeResponse(sc.nc, resp)
	sc.nc.SetWriteDeadline(time.Time{})
	sc.writeMu.Unlock()
}

// callHandler runs handler with a recover, so a panic deep in a single RPC
// (e.g. a bug in a new call/screen-share code path) logs a full stack trace
// and fails that one request instead of taking down the whole daemon - each
// request already runs on its own goroutine (see serve), and an unrecovered
// panic in any goroutine kills the entire process, not just that goroutine.
func (s *Server) callHandler(req Request, handler Handler) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("ipc: panic handling request", "method", req.Method, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("internal error handling %s: %v", req.Method, r)
		}
	}()
	return handler(req.Method, req.Params)
}

// Broadcast sends ev to every currently-connected client. Each connection is
// written to on its own goroutine with a bounded write deadline, so a client
// whose reader has stalled (blocking Write indefinitely, since a stalled
// reader doesn't itself make Write return an error) can't hold up delivery
// to every other connected client — Broadcast used to write sequentially on
// one goroutine, so one wedged client silently starved everyone else's
// events forever. A client that times out or errors is closed outright;
// its own read side observing the close will drop it from conns via serve's
// deferred cleanup.
func (s *Server) Broadcast(ev Event) {
	s.mu.Lock()
	conns := make([]*serverConn, 0, len(s.conns))
	for sc := range s.conns {
		conns = append(conns, sc)
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, sc := range conns {
		wg.Add(1)
		go func(sc *serverConn) {
			defer wg.Done()
			sc.writeMu.Lock()
			sc.nc.SetWriteDeadline(time.Now().Add(writeTimeout))
			err := writeEvent(sc.nc, ev)
			sc.nc.SetWriteDeadline(time.Time{})
			sc.writeMu.Unlock()
			if err != nil {
				sc.nc.Close()
			}
		}(sc)
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
