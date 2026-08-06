// Package ipc is the wire protocol between the kage TUI (a thin client) and
// the kage background daemon (--background mode), which owns all XMPP
// connections, storage, and decryption. It is a leaf package: it may be
// imported by ui-payload-shaped code in package main, but must never import
// ui, xmpp, config, or crypto itself, so it stays reusable from either side
// of the socket without pulling in either process's internals.
package ipc

import "encoding/json"

// Request is a client->daemon call. ID correlates it with its Response.
type Request struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response answers a Request with the same ID. Err is the stringified error
// (if any) since Go errors don't marshal across the wire on their own.
type Response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Err    string          `json:"err,omitempty"`
}

// Event is an unsolicited daemon->client push (e.g. an incoming message),
// broadcast to every attached client rather than answering a specific
// Request.
type Event struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data,omitempty"`
}
