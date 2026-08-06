//go:build !linux

package daemon

// launchTerminal is a no-op outside Linux; see the tray icon note in
// spawn_other.go — there's no tray to click in the first place.
func launchTerminal(configured string) {}
