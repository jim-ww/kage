package main

import (
	"log/slog"
	"reflect"

	tea "charm.land/bubbletea/v2"
	"github.com/jim-ww/kage/ipc"
)

// broadcast pushes ev to every attached TUI. ev must be one of events.
func broadcast(srv *ipc.Server, ev tea.Msg) {
	if srv == nil {
		return
	}
	if !isEvent[reflect.TypeOf(ev)] {
		slog.Error("broadcasting an unregistered event, dropped", "type", reflect.TypeOf(ev))
		return
	}
	srv.Broadcast(ev)
}
