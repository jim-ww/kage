//go:build !linux && !darwin

package filepicker

import (
	"os"
	"time"
)

// entryCtime has no ctime source on this platform via syscall.Stat_t
// (e.g. Windows exposes Win32FileAttributeData instead) — callers fall
// back to mtime.
func entryCtime(os.FileInfo) (time.Time, bool) {
	return time.Time{}, false
}
