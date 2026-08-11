//go:build !linux

package daemon

import "github.com/jim-ww/kage/config"

// EnsureRunning is a no-op outside Linux: the tray icon, single-instance
// locking, and ipc socket daemon relies on are only implemented there (see
// the _linux.go files) — kage's TUI itself doesn't currently work on other
// platforms either, since it now always depends on the background service.
func EnsureRunning(cfgPath string, debug bool) error {
	return nil
}

// Backend mirrors the Linux Backend interface so package main compiles
// unmodified on other platforms; Run never actually calls it.
type Backend interface {
	Reload(cfg config.Config)
}

// Run is a no-op outside Linux; see EnsureRunning.
func Run(cfg config.Config, backend Backend) error {
	return nil
}

// SignalReload is a no-op outside Linux; see EnsureRunning.
func SignalReload() error {
	return nil
}

// Status is a no-op outside Linux; see EnsureRunning.
func Status() (running bool, pid int) {
	return false, 0
}

// Stop is a no-op outside Linux; see EnsureRunning.
func Stop() (ok bool, err error) {
	return false, nil
}

// Notify is a no-op outside Linux; see EnsureRunning.
func Notify(title, body string) {}
