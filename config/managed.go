package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ManagedHeader is the first line of a config.toml written by the
// home-manager module (nix/hm-module.nix), which overwrites the file on
// every activation.
const ManagedHeader = "# Managed by home-manager (programs.kage)"

// ErrManaged is returned for a change to a config.toml that nix manages:
// written here, it would be overwritten by the next activation, or replace
// the symlink into the nix store and make that activation fail.
var ErrManaged = errors.New("config.toml is managed by home-manager - change it in your nix configuration (programs.kage)")

// CheckWritable reports ErrManaged if the config file at path is managed by
// nix: a symlink into the nix store, or a file starting with ManagedHeader.
func CheckWritable(path string) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if target, err := filepath.EvalSymlinks(path); err == nil && strings.HasPrefix(target, "/nix/store/") {
			return fmt.Errorf("%w (%s links to %s)", ErrManaged, path, target)
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadString('\n')
	if strings.HasPrefix(line, ManagedHeader) {
		return fmt.Errorf("%w (%s)", ErrManaged, path)
	}
	return nil
}
