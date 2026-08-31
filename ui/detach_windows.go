//go:build windows

package ui

import "syscall"

// detachedSysProcAttr: no session/process-group equivalent needed on
// Windows for this codepath.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return nil
}
