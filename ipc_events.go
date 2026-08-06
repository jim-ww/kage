package main

import (
	"encoding/json"
	"log/slog"

	"github.com/jim-ww/kage/ipc"
)

// broadcast marshals data and pushes it to every attached TUI client as kind
// — the daemon-side replacement for the old p.Send(ui.SomeMsg{...}) calls
// that talked to a single in-process tea.Program.
func broadcast(srv *ipc.Server, kind string, data any) {
	if srv == nil {
		return
	}
	b, err := json.Marshal(data)
	if err != nil {
		slog.Warn("marshaling event", "kind", kind, "err", err)
		return
	}
	srv.Broadcast(ipc.Event{Kind: kind, Data: b})
}
