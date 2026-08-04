//go:build !linux

package notifyd

import "github.com/jim-ww/kage/config"

// EnsureRunning is a no-op outside Linux: the tray icon and single-instance
// locking notifyd relies on are only implemented there (see the _linux.go
// files) — kage's TUI itself works fine on other platforms, it just won't
// get background desktop notifications.
func EnsureRunning(cfgPath string) error {
	return nil
}

// Run is a no-op outside Linux; see EnsureRunning.
func Run(cfg config.Config) error {
	return nil
}
