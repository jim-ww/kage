// Package ipc is the wire protocol between the kage TUI (a thin client) and
// the kage daemon, which owns all XMPP connections, storage, and decryption.
// Calls, replies and events are Go values sent as a gob stream; whoever uses
// the package registers (gob.Register) every concrete type it sends. It is a
// leaf package: it must never import ui, xmpp, config, or crypto itself.
package ipc

import "fmt"

// frame is everything that crosses the socket: a call (ID, Call), its reply
// (ID, Reply, Err) or an event (Event).
type frame struct {
	ID    uint64
	Call  any
	Reply any
	Err   string
	Event any
}

// callName names a call in logs.
func callName(call any) string { return fmt.Sprintf("%T", call) }
