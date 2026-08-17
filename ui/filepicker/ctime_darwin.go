package filepicker

import (
	"os"
	"syscall"
	"time"
)

// entryCtime returns info's ctime, if the platform exposes one via
// syscall.Stat_t.
func entryCtime(info os.FileInfo) (time.Time, bool) {
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(sys.Ctimespec.Sec, sys.Ctimespec.Nsec), true
}
