package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned by Call once the Conn's read loop has exited (the
// daemon hung up, or Close was called), including for any request already
// in flight at that point.
var ErrClosed = errors.New("ipc: connection closed")

// Conn is one client's connection to the daemon: it multiplexes outgoing
// Requests correlated by ID against incoming Responses, and delivers
// unsolicited Events to onEvent as they arrive.
type Conn struct {
	nc      net.Conn
	writeMu sync.Mutex
	nextID  atomic.Uint64

	pendingMu sync.Mutex
	pending   map[uint64]chan Response

	onEvent func(Event)

	closeOnce sync.Once
	closed    chan struct{}
}

// NewConn wraps nc as a Conn and starts its background read loop. onEvent is
// called synchronously from that read loop for every Event frame received —
// it must not block, since that would stall delivery of Responses on the
// same connection too.
func NewConn(nc net.Conn, onEvent func(Event)) *Conn {
	c := &Conn{
		nc:      nc,
		pending: make(map[uint64]chan Response),
		onEvent: onEvent,
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Dial connects to the daemon's socket at path and wraps it in a Conn.
func Dial(path string, onEvent func(Event)) (*Conn, error) {
	nc, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return NewConn(nc, onEvent), nil
}

func (c *Conn) readLoop() {
	defer c.shutdown()
	for {
		t, body, err := readFrame(c.nc)
		if err != nil {
			return
		}
		switch t {
		case tagResponse:
			var resp Response
			if err := json.Unmarshal(body, &resp); err != nil {
				continue
			}
			c.pendingMu.Lock()
			ch, ok := c.pending[resp.ID]
			if ok {
				delete(c.pending, resp.ID)
			}
			c.pendingMu.Unlock()
			if ok {
				ch <- resp
			}
		case tagEvent:
			var ev Event
			if err := json.Unmarshal(body, &ev); err != nil {
				continue
			}
			if c.onEvent != nil {
				c.onEvent(ev)
			}
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

// Call sends a request for method with params marshaled as its Params, blocks
// for the matching Response, and unmarshals its Result into result (which
// may be nil if the method has no return payload). It returns the daemon's
// error, reconstructed from Response.Err, if the call failed there.
func (c *Conn) Call(method string, params, result any) error {
	paramBytes, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("ipc: marshaling params for %s: %w", method, err)
	}

	id := c.nextID.Add(1)
	ch := make(chan Response, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	c.writeMu.Lock()
	err = writeRequest(c.nc, Request{ID: id, Method: method, Params: paramBytes})
	c.writeMu.Unlock()
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return fmt.Errorf("ipc: sending request %s: %w", method, err)
	}

	resp, ok := <-ch
	if !ok {
		return ErrClosed
	}
	if resp.Err != "" {
		return errors.New(resp.Err)
	}
	if result == nil || len(resp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Result, result); err != nil {
		return fmt.Errorf("ipc: unmarshaling result of %s: %w", method, err)
	}
	return nil
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

var _ io.Closer = (*Conn)(nil)
