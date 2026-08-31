//go:build !windows

package ui

import "syscall"

// detachedSysProcAttr keeps a spawned viewer (xdg-open, and whatever it
// execs) out of kage's process group, so a signal sent to kage's group
// (Ctrl-C, terminal hangup) doesn't take the viewer down with it.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
