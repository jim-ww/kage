package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
)

// Contact avatars (XEP-0084), daemon side. The daemon owns the fetching,
// as it owns every other network-facing thing, and drops the image into a
// cache directory both processes can read — so the TUI never receives image
// bytes over the socket, only a "this file changed" push (see evAvatar).
//
// The cached file's own SHA-1 is the cache key: XEP-0084 makes the data
// node's item id the hash of its content, so comparing the two is the whole
// of the freshness check, with no separate index to keep in sync.

// avatarCacheDir is where fetched avatars live, one file per bare JID.
// Created on demand.
func avatarCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "kage", "avatars")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// avatarFileName is the cache file for a bare JID. A bare JID can't contain
// a path separator (that's the resource delimiter), but it arrives off the
// network, so separators and dot segments are replaced rather than trusted.
func avatarFileName(bare, ext string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == os.PathSeparator {
			return '_'
		}
		return r
	}, bare)
	if safe == "" || safe == "." || safe == ".." {
		return ""
	}
	return safe + ext
}

// avatarExt maps a published media type to the file extension the TUI's
// image decoder keys off. Anything else is refused: we only render what
// image.Decode is registered for.
func avatarExt(mediaType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png", "":
		// Empty type is legal-ish in the wild and overwhelmingly PNG; the
		// decoder sniffs content anyway, so a wrong guess costs a decode
		// error rather than a bad render.
		return ".png", true
	case "image/jpeg", "image/jpg":
		return ".jpg", true
	default:
		return "", false
	}
}

// cachedAvatarHash is the SHA-1 of whatever avatar we already hold for
// bare, or "" if we hold none. That hash is exactly the item id XEP-0084
// publishes, so it can be compared to the metadata directly.
func cachedAvatarHash(dir, bare string) string {
	for _, ext := range []string{".png", ".jpg"} {
		name := avatarFileName(bare, ext)
		if name == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		sum := sha1.Sum(data)
		return hex.EncodeToString(sum[:])
	}
	return ""
}

// syncAvatar brings the cached avatar for one contact up to date and, if it
// changed, tells every attached TUI where to find it. Best effort
// throughout: a contact with no avatar, a server that refuses the node, an
// unsupported image type — all of it just leaves the monogram in place, so
// nothing here is worth failing an account's event loop over.
func (s *accountSession) syncAvatar(ctx context.Context, srv *ipc.Server, accountIdx int, bare string) {
	if bare == "" || !avatarsEnabled.Load() {
		return
	}
	client := s.client.Load()
	if client == nil {
		return
	}
	dir, err := avatarCacheDir()
	if err != nil {
		slog.Debug("avatar cache dir", "err", err)
		return
	}

	info, ok, err := client.FetchAvatarMetadata(ctx, bare)
	if err != nil {
		// The overwhelmingly common case is a contact who publishes no
		// avatar node at all, which the server reports as an error.
		slog.Debug("fetching avatar metadata", "jid", s.account.JID, "from", bare, "err", err)
		return
	}
	if !ok || info.ID == "" {
		return
	}
	if info.Bytes > xmpp.AvatarMaxBytes {
		slog.Debug("skipping oversized avatar", "from", bare, "bytes", info.Bytes)
		return
	}
	if cachedAvatarHash(dir, bare) == info.ID {
		return // already have exactly this one
	}
	ext, supported := avatarExt(info.Type)
	if !supported {
		slog.Debug("skipping avatar of unsupported type", "from", bare, "type", info.Type)
		return
	}
	name := avatarFileName(bare, ext)
	if name == "" {
		return
	}

	data, err := client.FetchAvatarData(ctx, bare, info.ID)
	if err != nil {
		slog.Debug("fetching avatar data", "jid", s.account.JID, "from", bare, "err", err)
		return
	}
	// The item id is supposed to be the SHA-1 of these bytes. A mismatch
	// means the contact's own client is wrong about what it published, and
	// the hash is what our freshness check keys on — caching it would make
	// us re-fetch forever.
	sum := sha1.Sum(data)
	if got := hex.EncodeToString(sum[:]); got != info.ID {
		slog.Debug("avatar data does not match its advertised hash", "from", bare, "advertised", info.ID, "got", got)
		return
	}

	path := filepath.Join(dir, name)
	if err := writeFileAtomic(path, data); err != nil {
		slog.Warn("writing avatar cache", "from", bare, "err", err)
		return
	}
	// A contact who switched image formats leaves the old file behind, and
	// LoadAvatarDir would load whichever it reached last.
	for _, other := range []string{".png", ".jpg"} {
		if other == ext {
			continue
		}
		if stale := avatarFileName(bare, other); stale != "" {
			os.Remove(filepath.Join(dir, stale))
		}
	}

	slog.Debug("cached avatar", "jid", s.account.JID, "from", bare, "id", info.ID, "bytes", len(data))
	broadcast(srv, evAvatar, ui.AvatarUpdatedMsg{AccountIdx: accountIdx, JID: bare, Path: path})
}

// writeFileAtomic writes via a temp file in the same directory and renames,
// so a TUI reading the cache never sees a half-written image.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".avatar-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming avatar into place: %w", err)
	}
	return nil
}

// avatarChecked remembers which contacts this daemon run has already asked
// about, so the sync that rides on presence doesn't re-query a contact's
// metadata node every time they change status. Changes still arrive
// promptly: a contact publishing a new avatar sends a PEP push, which goes
// through syncAvatar directly rather than this.
var avatarChecked sync.Map // accountJID + "\x00" + bare -> struct{}

// syncAvatarOnce is syncAvatar for triggers that repeat on their own — a
// contact coming online, say — and so must not mean a fetch each time.
func (s *accountSession) syncAvatarOnce(ctx context.Context, srv *ipc.Server, accountIdx int, bare string) {
	if bare == "" {
		return
	}
	if _, seen := avatarChecked.LoadOrStore(s.account.JID+"\x00"+bare, struct{}{}); seen {
		return
	}
	s.syncAvatar(ctx, srv, accountIdx, bare)
}
