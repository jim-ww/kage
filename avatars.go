package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
)

// Contact avatars (XEP-0084), daemon side. The daemon owns the fetching,
// as it owns every other network-facing thing, and drops the image into a
// cache directory both processes can read — so the TUI never receives image
// bytes over the socket, only a "this file changed" push (see AvatarUpdatedMsg).
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
		slog.Debug("no avatar published", "jid", s.account.JID, "from", bare)
		// They publish no avatar (anymore): drop the one we still hold,
		// or it would stay on screen forever.
		if cachedAvatarHash(dir, bare) != "" {
			removeCachedAvatar(bare)
			slog.Debug("dropped avatar the contact no longer publishes", "jid", s.account.JID, "from", bare)
			broadcast(srv, ui.AvatarUpdatedMsg{AccountIdx: accountIdx, JID: bare, Path: ""})
		}
		return
	}
	if info.Bytes > xmpp.AvatarMaxBytes {
		slog.Debug("skipping oversized avatar", "from", bare, "bytes", info.Bytes)
		return
	}
	if cachedAvatarHash(dir, bare) == info.ID {
		slog.Debug("avatar already cached", "jid", s.account.JID, "from", bare, "id", info.ID)
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
	broadcast(srv, ui.AvatarUpdatedMsg{AccountIdx: accountIdx, JID: bare, Path: path})
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

// SetOwnAvatar publishes a local image file as this account's avatar
// (XEP-0084) and caches it locally under the account's own JID, so the
// account bar and any chat with oneself show it without waiting for a
// round trip back from the server.
func (a *adapter) SetOwnAvatar(accountIdx int, path string) error {
	s, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading avatar: %w", err)
	}
	if len(data) > xmpp.AvatarMaxBytes {
		return fmt.Errorf("avatar is %s, over the %s limit",
			humanBytes(len(data)), humanBytes(xmpp.AvatarMaxBytes))
	}
	// Decoded rather than trusted by extension: the media type we publish
	// is what contacts decode by, and the dimensions are part of the
	// metadata. A file that doesn't decode here would have been a broken
	// avatar for everyone who fetched it.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("reading avatar: not a usable image: %w", err)
	}
	var mediaType string
	switch format {
	case "png":
		mediaType = "image/png"
	case "jpeg":
		mediaType = "image/jpeg"
	default:
		return fmt.Errorf("avatar is %s; only PNG and JPEG can be published", format)
	}

	id, err := client.PublishAvatar(context.Background(), data, mediaType, cfg.Width, cfg.Height)
	if err != nil {
		return err
	}

	bare := bareJID(s.account.JID)
	if err := storeAvatar(bare, data, mediaType); err != nil {
		// Published fine; only the local copy failed, so say so rather
		// than reporting the publish as failed.
		slog.Warn("caching own avatar", "jid", bare, "err", err)
		return nil
	}
	slog.Debug("published own avatar", "jid", bare, "id", id, "bytes", len(data))
	if path, ok := cachedAvatarPath(bare); ok {
		broadcast(a.srv, ui.AvatarUpdatedMsg{AccountIdx: accountIdx, JID: bare, Path: path})
	}
	return nil
}

// ownAvatarSyncTimeout bounds SyncOwnAvatar's two PEP round trips.
const ownAvatarSyncTimeout = 15 * time.Second

// SyncOwnAvatar fetches this account's published avatar into the cache -
// one set from another client is otherwise never fetched, as no presence
// of ours comes online the way a contact's does.
func (a *adapter) SyncOwnAvatar(accountIdx int) error {
	s, _, err := a.online(accountIdx)
	if err != nil {
		slog.Debug("syncing own avatar", "account", accountIdx, "err", err)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ownAvatarSyncTimeout)
	defer cancel()
	s.syncAvatar(ctx, a.srv, accountIdx, bareJID(s.account.JID))
	return nil
}

// RemoveOwnAvatar stops publishing this account's avatar and drops the
// local copy.
func (a *adapter) RemoveOwnAvatar(accountIdx int) error {
	s, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}
	if err := client.DeleteAvatar(context.Background()); err != nil {
		return err
	}

	bare := bareJID(s.account.JID)
	removeCachedAvatar(bare)
	broadcast(a.srv, ui.AvatarUpdatedMsg{AccountIdx: accountIdx, JID: bare, Path: ""})
	return nil
}

// storeAvatar writes one contact's (or our own) avatar into the cache,
// clearing whichever other extension they previously used.
func storeAvatar(bare string, data []byte, mediaType string) error {
	dir, err := avatarCacheDir()
	if err != nil {
		return err
	}
	ext, ok := avatarExt(mediaType)
	if !ok {
		return fmt.Errorf("unsupported avatar type %q", mediaType)
	}
	name := avatarFileName(bare, ext)
	if name == "" {
		return fmt.Errorf("unusable jid %q", bare)
	}
	if err := writeFileAtomic(filepath.Join(dir, name), data); err != nil {
		return err
	}
	for _, other := range []string{".png", ".jpg"} {
		if other == ext {
			continue
		}
		if stale := avatarFileName(bare, other); stale != "" {
			os.Remove(filepath.Join(dir, stale))
		}
	}
	return nil
}

// cachedAvatarPath is where a contact's cached avatar lives, if it has one.
func cachedAvatarPath(bare string) (string, bool) {
	dir, err := avatarCacheDir()
	if err != nil {
		return "", false
	}
	for _, ext := range []string{".png", ".jpg"} {
		name := avatarFileName(bare, ext)
		if name == "" {
			continue
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}

func removeCachedAvatar(bare string) {
	dir, err := avatarCacheDir()
	if err != nil {
		return
	}
	for _, ext := range []string{".png", ".jpg"} {
		if name := avatarFileName(bare, ext); name != "" {
			os.Remove(filepath.Join(dir, name))
		}
	}
}

// pruneAvatarCache deletes cached avatars nobody is shown any more: of
// contacts removed from every roster, or of removed accounts. The cache is
// keyed by bare JID alone, shared by every account, so a file is kept while
// any configured account still has its owner as a contact or is that JID.
// Run once at daemon start; a failure to read a roster keeps everything.
func pruneAvatarCache(ctx context.Context, db *storage.Queries, accounts []config.Account) {
	dir, err := avatarCacheDir()
	if err != nil {
		return
	}
	keep := make(map[string]bool)
	for _, acct := range accounts {
		keep[bareJID(acct.JID)] = true
		rows, err := db.ListRoster(ctx, acct.JID)
		if err != nil {
			slog.Warn("pruning avatar cache: loading roster", "jid", acct.JID, "err", err)
			return
		}
		for _, r := range rows {
			keep[r.Jid] = true
		}
	}
	for _, name := range staleAvatarFiles(dir, keep) {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			slog.Debug("pruning avatar cache", "file", name, "err", err)
		}
	}
}

// staleAvatarFiles lists the avatar files in dir whose JID isn't in keep.
// Anything not named like an avatar (a temp file mid-write) is left alone.
func staleAvatarFiles(dir string, keep map[string]bool) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	kept := make(map[string]bool, 2*len(keep))
	for bare := range keep {
		for _, ext := range []string{".png", ".jpg"} {
			if name := avatarFileName(bare, ext); name != "" {
				kept[name] = true
			}
		}
	}
	var stale []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || kept[name] {
			continue
		}
		if ext := filepath.Ext(name); ext != ".png" && ext != ".jpg" {
			continue
		}
		stale = append(stale, name)
	}
	return stale
}

// humanBytes renders a byte count for an error a person reads.
func humanBytes(n int) string {
	if n < 1<<10 {
		return fmt.Sprintf("%d bytes", n)
	}
	if n < 1<<20 {
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
