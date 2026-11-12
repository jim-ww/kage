package ipc

import (
	"bufio"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed is returned by Call once the Conn's read loop has exited (the
// daemon hung up, or Close was called), including for any call already in
// flight at that point.
var ErrClosed = errors.New("ipc: connection closed")

// Conn is one client's connection to the daemon: it correlates calls with
// their replies by ID, and delivers events to onEvent as they arrive.
type Conn struct {
	nc      net.Conn
	writeMu sync.Mutex
	enc     *gob.Encoder
	nextID  atomic.Uint64

	pendingMu sync.Mutex
	pending   map[uint64]chan frame

	onEvent func(any)

	closeOnce sync.Once
	closed    chan struct{}
}

// NewConn wraps nc as a Conn and starts its read loop. onEvent is called
// from that read loop for every event received - it must not block, since
// that would stall replies on the same connection too.
func NewConn(nc net.Conn, onEvent func(any)) *Conn {
	c := &Conn{
		nc:      nc,
		enc:     gob.NewEncoder(nc),
		pending: make(map[uint64]chan frame),
		onEvent: onEvent,
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Dial connects to the daemon's socket at path and wraps it in a Conn.
func Dial(path string, onEvent func(any)) (*Conn, error) {
	nc, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return NewConn(nc, onEvent), nil
}

func (c *Conn) readLoop() {
	defer c.shutdown()
	dec := gob.NewDecoder(bufio.NewReader(c.nc))
	for {
		var f frame
		if err := dec.Decode(&f); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				slog.Warn("ipc: reading from daemon", "err", err)
			}
			return
		}
		if f.Event != nil {
			if c.onEvent != nil {
				c.onEvent(f.Event)
			}
			continue
		}
		c.pendingMu.Lock()
		ch, ok := c.pending[f.ID]
		delete(c.pending, f.ID)
		c.pendingMu.Unlock()
		if ok {
			ch <- f
		}
	}
}

func (c *Conn) shutdown() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.pendingMu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()
	})
}

// Call sends call to the daemon and blocks for its reply, returning the
// daemon's error if the call failed there.
func (c *Conn) Call(call any) (any, error) {
	start := time.Now()
	id := c.nextID.Add(1)
	ch := make(chan frame, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	c.writeMu.Lock()
	err := c.enc.Encode(frame{ID: id, Call: call})
	c.writeMu.Unlock()
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("ipc: sending %s: %w", callName(call), err)
	}

	f, ok := <-ch
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		// A half-open socket (laptop sleep/resume) never errors, it just
		// stops flowing, and this receive has no deadline: this log is the
		// only trace of a call wedged that way.
		slog.Warn("ipc: call took unusually long", "call", callName(call), "elapsed", elapsed, "ok", ok)
	}
	if !ok {
		return nil, ErrClosed
	}
	if f.Err != "" {
		return f.Reply, errors.New(f.Err)
	}
	return f.Reply, nil
}

// Done returns a channel closed once the connection's read loop has exited,
// letting a caller detect disconnection without a failed Call.
func (c *Conn) Done() <-chan struct{} { return c.closed }

// Close closes the underlying connection; ongoing/future Calls return
// ErrClosed.
func (c *Conn) Close() error {
	err := c.nc.Close()
	c.shutdown()
	return err
}
