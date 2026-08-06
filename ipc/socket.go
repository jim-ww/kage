package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// SocketPath returns the Unix domain socket path the daemon listens on and
// clients dial — alongside daemon's existing lock file, in the same
// per-user config directory.
func SocketPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "kage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "kage.sock"), nil
}

// Listen opens the daemon's socket at path, removing a stale socket file left
// behind by a previous, no-longer-running daemon. A "stale" socket is one
// nothing accepts a connection on; if something does answer, this is a
// second daemon starting up wrongly and Listen fails rather than stealing
// the path out from under it.
func Listen(path string) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err == nil {
		return ln, nil
	}
	if !errors.Is(err, os.ErrExist) && !isAddrInUse(err) {
		return nil, err
	}

	if c, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond); dialErr == nil {
		c.Close()
		return nil, fmt.Errorf("ipc: socket %s already has a live listener", path)
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("ipc: removing stale socket %s: %w", path, err)
	}
	return net.Listen("unix", path)
}

func isAddrInUse(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// DialTimeout connects to the daemon's socket at path, failing fast if
// nothing is listening — used both for the one-shot "is a daemon already
// running" probe and, wrapped in retry/backoff by the caller, right after
// spawning one.
func DialTimeout(path string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", path, timeout)
}
