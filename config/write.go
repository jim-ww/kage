package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// fileMu serializes every read-modify-write of config.toml/state.toml in this
// process. Each Set* helper below loads the whole file, edits one field and
// writes it back, and the daemon runs each RPC on its own goroutine with
// several TUI clients allowed to be attached at once (see ipc.Server.Accept)
// — so two of them landing together (one dragging the sidebar while another
// opens a chat, say) would otherwise both write from the same loaded copy and
// silently drop one of the two edits.
var fileMu sync.Mutex

// updateConfigFile applies mutate to the config file at path and writes it
// back, holding fileMu for the whole read-modify-write.
func updateConfigFile(path string, mutate func(*Config) error) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	cfg, err := loadOrEmpty(path)
	if err != nil {
		return err
	}
	if err := mutate(&cfg); err != nil {
		return err
	}
	return writeFileConfig(path, cfg)
}

// updateStateFile applies mutate to the state file next to path and writes it
// back, holding fileMu for the whole read-modify-write.
func updateStateFile(path string, mutate func(*State) error) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	st, err := loadState(path)
	if err != nil {
		return err
	}
	if err := mutate(&st); err != nil {
		return err
	}
	return writeState(st)
}

// writeFileAtomic writes data to path via a temp file in the same directory
// plus a rename, instead of os.WriteFile's truncate-then-write. The truncated
// window is short but it's a window in which the file parses as empty or
// half-written TOML, and whoever reads it then (another kage process starting
// up, or this one reloading on SIGHUP) sees every setting in it — configured
// accounts included — as gone, with nothing to distinguish that from a user
// who really did remove them.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", path, err)
	}
	tmp := f.Name()
	// Removed on every failure path; a no-op once the rename below succeeded.
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("setting permissions on %s: %w", tmp, err)
	}
	// Without the fsync, a crash right after the rename can leave the renamed
	// file present but empty — worse than the old content it replaced.
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("syncing %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}
