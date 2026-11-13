package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jim-ww/kage/crypto/localstore"
	kageomemo "github.com/jim-ww/kage/crypto/omemo"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// omemoPeerProtocolTTL bounds how long a cached peer-protocol negotiation
// result (storage.omemoPeerProtocol) is trusted before resolveOmemoProtocol
// re-probes - a peer might upgrade clients between conversations.
const omemoPeerProtocolTTL = 7 * 24 * time.Hour

// sealBody seals plaintext under localKey, the local storage key. With no
// key (no local storage password set), or if sealing fails, the body is
// stored as-is and encrypted comes back false.
func sealBody(localKey []byte, plaintext string) (body string, encrypted bool) {
	if localKey == nil {
		return plaintext, false
	}
	ct, err := localstore.Seal(localKey, plaintext)
	if err != nil {
		slog.Warn("encrypting for storage", "err", err)
		return plaintext, false
	}
	return ct, true
}

// openBody is sealBody's reverse.
func openBody(localKey []byte, body string, encrypted bool) (string, error) {
	if !encrypted {
		return body, nil
	}
	if localKey == nil {
		return "", errors.New("stored encrypted, but no local storage password is available")
	}
	return localstore.Open(localKey, body)
}

// encryptForStorage is sealBody for a message row's body.
func encryptForStorage(s *accountSession, plaintext string) (body sql.NullString, encrypted bool) {
	ct, encrypted := sealBody(s.localKey, plaintext)
	return sql.NullString{String: ct, Valid: true}, encrypted
}

// loadDraft returns row's draft, sealing and storing a plaintext one when
// there's a key, as readStoredBody does. One that can't be opened is "".
func loadDraft(ctx context.Context, queries *storage.Queries, accountJID string, row storage.ListChatDraftsRow, localKey []byte) string {
	if !row.Encrypted {
		if ct, sealed := sealBody(localKey, row.Body); sealed {
			if err := queries.SetChatDraft(ctx, storage.SetChatDraftParams{
				AccountJid: accountJID, RosterJid: row.Rosterjid, Body: ct, Encrypted: true,
			}); err != nil {
				slog.Warn("encrypting stored draft", "err", err)
			}
		}
		return row.Body
	}
	pt, err := openBody(localKey, row.Body, true)
	if err != nil {
		slog.Warn("decrypting stored draft", "err", err)
	}
	return pt
}

// decryptOutboxBody returns row's plaintext body, or "" with a warning
// logged if it can't be opened, rather than blocking startup/flush.
func decryptOutboxBody(row storage.Outbox, localKey []byte) string {
	pt, err := openBody(localKey, row.Body, row.Encrypted)
	if err != nil {
		slog.Warn("decrypting stored outbox entry", "to", row.Toattr, "err", err)
	}
	return pt
}

// outboxRowToMessage is a queued or failed outbox row as the placeholder the
// TUI shows. An attachment's URL isn't known yet, so its file name shows.
func outboxRowToMessage(row storage.Outbox, localKey []byte) ui.Message {
	content := decryptOutboxBody(row, localKey)
	if row.Filepath.Valid && row.Filepath.String != "" {
		name := filepath.Base(row.Filepath.String)
		if content != "" {
			content += "\n[queued: " + name + "]"
		} else {
			content = "[queued: " + name + "]"
		}
	}
	return ui.Message{
		LocalID: row.Localid,
		Author:  "me",
		Content: content,
		SentAt:  time.Unix(row.Createdat, 0),
		IsMe:    true,
		Pending: !row.Failed,
		Failed:  row.Failed,
	}
}

// publishOwnGPGKey publishes our public key over PEP (XEP-0373) for contacts
// to find. Best-effort; they can still configure gpg_peers.
func publishOwnGPGKey(ctx context.Context, s *accountSession) {
	keyData, err := s.gpg.Export(s.account.GPGKeyID)
	if err != nil {
		slog.Warn("exporting own gpg key", "err", err)
		return
	}
	if err := s.client.Load().PublishOpenPGPKey(ctx, s.account.GPGKeyID, keyData); err != nil {
		slog.Warn("publishing gpg key via PEP (XEP-0373)", "err", err)
		return
	}
}

// setupOmemo loads or generates the account's OMEMO identities, builds a
// Manager per protocol on the dialed client, and publishes bundles and
// device lists. New devices are trusted on first use.
func setupOmemo(ctx context.Context, s *accountSession) {
	client := s.client.Load()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		storeOmemoManager(s, &s.omemoMgrV2, omemolib.ProtocolV2, setupOmemoProtocol(
			ctx, s, client, omemolib.ProtocolV2, client.OmemoTransport(),
			client.FetchOmemoDeviceList, client.PublishOmemoDeviceList))
	}()
	go func() {
		defer wg.Done()
		storeOmemoManager(s, &s.omemoMgrV1, omemolib.ProtocolV1, setupOmemoProtocol(
			ctx, s, client, omemolib.ProtocolV1, client.OmemoTransportV1(),
			client.FetchOmemoDeviceListV1, client.PublishOmemoDeviceListV1))
	}()
	wg.Wait()
	s.signalOmemoReady()
}

// storeOmemoManager stores a newly built Manager, keeping the previous one
// when setup failed (nil): a stale Manager only fails network operations
// until the next setup, while none decrypts nothing.
func storeOmemoManager(
	s *accountSession, slot *atomic.Pointer[omemolib.Manager],
	protocol omemolib.Protocol, mgr *omemolib.Manager,
) {
	if mgr != nil {
		slot.Store(mgr)
		return
	}
	if slot.Load() != nil {
		slog.Warn("omemo setup failed; keeping the previously working manager",
			"protocol", protocol, "jid", s.account.JID)
	}
}

// omemoV1 returns this account's legacy-protocol OMEMO Manager, or nil if
// setup hasn't completed (or has never succeeded) for it.
func (s *accountSession) omemoV1() *omemolib.Manager { return s.omemoMgrV1.Load() }

// omemoV2 returns this account's XEP-0384 OMEMO Manager, or nil if setup
// hasn't completed (or has never succeeded) for it.
func (s *accountSession) omemoV2() *omemolib.Manager { return s.omemoMgrV2.Load() }

// setupOmemoProtocol is setupOmemo's per-protocol worker, shared by
// ProtocolV2 (XEP-0384) and ProtocolV1 (legacy) since the setup steps are
// identical modulo which Transport/PEP wire format is used.
func setupOmemoProtocol(
	ctx context.Context, s *accountSession, client *xmpp.Client, protocol omemolib.Protocol,
	transport omemolib.Transport,
	fetchDeviceList func(context.Context, string) (omemolib.DeviceList, error),
	publishDeviceList func(context.Context, omemolib.DeviceList) error,
) *omemolib.Manager {
	store := kageomemo.NewStore(s.db, s.account.JID, protocol)

	if _, err := store.IdentityKeyPair(ctx); err != nil {
		// No rows: no identity yet, so generate one. Anything else is the
		// database failing, which is fatal rather than going on without
		// encryption.
		if !errors.Is(err, sql.ErrNoRows) {
			log.Fatalf("fatal: loading omemo(%s) identity for %s: %v", protocol, s.account.JID, err)
		}

		id, err := omemolib.GenerateDeviceID()
		if err != nil {
			slog.Warn("generating omemo device id", "protocol", protocol, "jid", s.account.JID, "err", err)
			return nil
		}
		if err := omemolib.InitIdentity(ctx, store, s.account.JID, id, protocol); err != nil {
			// The database failing, as above.
			log.Fatalf("fatal: initializing omemo(%s) identity for %s: %v", protocol, s.account.JID, err)
		}
	}

	mgr, err := omemolib.NewManager(ctx, store, transport, protocol,
		omemolib.WithTrustResolver(func(context.Context, omemolib.Device, []byte) error { return nil }))
	if err != nil {
		slog.Warn("setting up omemo manager", "protocol", protocol, "jid", s.account.JID, "err", err)
		return nil
	}

	// Must happen before the bundle publish below, so the publish lands on
	// the new device's node rather than one we're about to abandon.
	rotatedFrom := rotateOutOfRangeDeviceID(ctx, s, protocol, mgr)

	if err := mgr.PublishBundle(ctx); err != nil {
		slog.Warn("publishing omemo bundle", "protocol", protocol, "jid", s.account.JID, "err", err)
	}

	// One-time prekeys are used up by new sessions and only made here:
	// replenished and republished on every connect.
	if needsMore, err := mgr.NeedsMorePreKeys(ctx); err != nil {
		slog.Warn("checking omemo prekey count", "protocol", protocol, "jid", s.account.JID, "err", err)
	} else if needsMore {
		if err := mgr.GenerateOneTimePreKeys(ctx, omemolib.DefaultPreKeyCount); err != nil {
			slog.Warn("replenishing omemo prekeys", "protocol", protocol, "jid", s.account.JID, "err", err)
		} else if err := mgr.PublishBundle(ctx); err != nil {
			slog.Warn("republishing omemo bundle after prekey replenishment", "protocol", protocol, "jid", s.account.JID, "err", err)
		} else {
			slog.Debug("omemo prekeys replenished", "protocol", protocol, "jid", s.account.JID, "count", omemolib.DefaultPreKeyCount)
		}
	}

	devices, err := fetchDeviceList(ctx, s.account.JID)
	if err != nil {
		slog.Warn("fetching own omemo device list", "protocol", protocol, "jid", s.account.JID, "err", err)
		return mgr
	}
	local := mgr.LocalDevice().ID
	slog.Debug("omemo setup: local device ID and current device list", "protocol", protocol, "jid", s.account.JID, "local_device", local, "devices", devices.Devices)
	kept, alreadyListed := pruneOwnDeviceList(devices.Devices, local, rotatedFrom, protocol, s.account.JID)
	devices.Devices = kept
	// Republished even when listed: publishing is what makes the node open
	// (makeNodeOpen), in case that failed before.
	devices.JID = s.account.JID
	if !alreadyListed {
		devices.Devices = append(devices.Devices, local)
	}
	slog.Debug("omemo setup: publishing device list", "protocol", protocol, "jid", s.account.JID, "devices", devices.Devices)
	if err := publishDeviceList(ctx, devices); err != nil {
		slog.Warn("publishing omemo device list", "protocol", protocol, "jid", s.account.JID, "err", err)
	} else if rotatedFrom != 0 {
		// Only once peers have been told to stop using it: the old bundle
		// going away before the new device list is published would leave a
		// window where a peer resolves the abandoned ID and finds nothing.
		deleteOmemoBundle(ctx, client, protocol, rotatedFrom)
	}

	// The fetch above bypassed mgr's device cache, which encryption uses:
	// sync it for our own JID, as resyncPeerDeviceLists does for contacts,
	// or our other devices added meanwhile get no key.
	if err := mgr.SyncDevices(ctx, s.account.JID); err != nil {
		slog.Warn("resyncing own omemo device list on connect", "protocol", protocol, "jid", s.account.JID, "err", err)
	}
	return mgr
}

// omemoManagerFor returns s's Manager for protocol, or nil if that
// protocol's setup hasn't completed (or failed) for this account.
func (s *accountSession) omemoManagerFor(protocol omemolib.Protocol) *omemolib.Manager {
	if protocol == omemolib.ProtocolV1 {
		return s.omemoV1()
	}
	return s.omemoV2()
}

// resolveEncryptionMode is peerJID's chat's outgoing encryption, or the
// default.
func resolveEncryptionMode(ctx context.Context, s *accountSession, peerJID string) string {
	mode, err := s.db.GetChatEncryptionMode(ctx, storage.GetChatEncryptionModeParams{
		AccountJid: s.account.JID,
		RosterJid:  peerJID,
	})
	if err != nil || mode == "" {
		return currentDefaultEncryptionMode()
	}
	return mode
}

// resolveOmemoProtocol picks peerJID's OMEMO protocol: omemo_peers, else the
// cached result within omemoPeerProtocolTTL, else whichever device list the
// peer publishes, v2 first, cached.
func resolveOmemoProtocol(ctx context.Context, s *accountSession, peerJID string) omemolib.Protocol {
	if override := s.account.OmemoPeers[peerJID]; override != "" {
		if override == "v1" {
			return omemolib.ProtocolV1
		}
		return omemolib.ProtocolV2
	}

	if row, err := s.db.GetOmemoPeerProtocol(ctx, storage.GetOmemoPeerProtocolParams{
		AccountJid: s.account.JID, PeerJid: peerJID,
	}); err == nil {
		if time.Since(time.Unix(row.Probedat, 0)) < omemoPeerProtocolTTL {
			return protocolFromString(row.Protocol)
		}
	}

	client := s.client.Load()
	v2Devices, v2Err := client.FetchOmemoDeviceList(ctx, peerJID)
	v1Devices, v1Err := client.FetchOmemoDeviceListV1(ctx, peerJID)

	protocol := omemolib.ProtocolV2
	switch {
	case v2Err == nil && len(v2Devices.Devices) > 0:
		protocol = omemolib.ProtocolV2
	case v1Err == nil && len(v1Devices.Devices) > 0:
		protocol = omemolib.ProtocolV1
	}

	if err := s.db.SetOmemoPeerProtocol(ctx, storage.SetOmemoPeerProtocolParams{
		AccountJid: s.account.JID,
		PeerJid:    peerJID,
		Protocol:   protocolString(protocol),
		ProbedAt:   time.Now().Unix(),
	}); err != nil {
		slog.Warn("caching omemo protocol negotiation", "peer", peerJID, "err", err)
	}
	return protocol
}

func protocolString(p omemolib.Protocol) string {
	if p == omemolib.ProtocolV1 {
		return "v1"
	}
	return "v2"
}

func protocolFromString(s string) omemolib.Protocol {
	if s == "v1" {
		return omemolib.ProtocolV1
	}
	return omemolib.ProtocolV2
}

// resolveOmemoManagerForMode is the protocol and Manager for mode; another
// mode, an old stored one, picks by resolveOmemoProtocol.
func resolveOmemoManagerForMode(ctx context.Context, s *accountSession, mode, peerJID string) (omemolib.Protocol, *omemolib.Manager) {
	var protocol omemolib.Protocol
	switch mode {
	case "omemo-v1":
		protocol = omemolib.ProtocolV1
	case "omemo-v2":
		protocol = omemolib.ProtocolV2
	default:
		protocol = resolveOmemoProtocol(ctx, s, peerJID)
	}
	return protocol, s.omemoManagerFor(protocol)
}

// resolvePeerKey is peerJID's GPG key fingerprint, "" if none: gpg_peers,
// else the cached one, else found over PEP (XEP-0373) and cached.
func resolvePeerKey(ctx context.Context, s *accountSession, peerJID string) string {
	if key := s.account.GPGPeers[peerJID]; key != "" {
		return key
	}

	if fpr, err := s.db.GetPGPPeerKey(ctx, storage.GetPGPPeerKeyParams{AccountJid: s.account.JID, Jid: peerJID}); err == nil && fpr != "" {
		return fpr
	}

	fpr, err := discoverPeerKey(ctx, s, peerJID)
	if err != nil {
		slog.Info("no gpg key found; sending unencrypted", "peer", peerJID, "err", err)
		return ""
	}
	if err := s.db.UpsertPGPPeerKey(ctx, storage.UpsertPGPPeerKeyParams{AccountJid: s.account.JID, Jid: peerJID, Fingerprint: fpr}); err != nil {
		slog.Warn("caching discovered gpg key", "peer", peerJID, "err", err)
	}
	return fpr
}

// discoverPeerKey fetches peerJID's most-recently-published OpenPGP key via
// their PEP nodes (XEP-0373) and imports it into the local keyring.
func discoverPeerKey(ctx context.Context, s *accountSession, peerJID string) (string, error) {
	client := s.client.Load()
	fprs, err := client.FetchOpenPGPFingerprints(ctx, peerJID)
	if err != nil {
		return "", err
	}
	if len(fprs) == 0 {
		return "", fmt.Errorf("no key published")
	}
	fpr := fprs[0]

	keyData, err := client.FetchOpenPGPKey(ctx, peerJID, fpr)
	if err != nil {
		return "", err
	}
	if err := s.gpg.Import(keyData, fpr); err != nil {
		return "", err
	}
	return fpr, nil
}

// rotateOutOfRangeDeviceID re-numbers the account's OMEMO device if its ID
// is outside 1..2^31-1, returning the old ID (0 if none) to prune.
// libsignal-based clients parse device IDs as signed int32 and drop the
// whole stanza on overflow. The identity key, and so its fingerprint, is
// kept (see omemolib.RotateDeviceID).
func rotateOutOfRangeDeviceID(
	ctx context.Context, s *accountSession, protocol omemolib.Protocol, mgr *omemolib.Manager,
) omemolib.DeviceID {
	old := mgr.LocalDevice().ID
	if old.Valid() {
		return 0
	}
	next, err := omemolib.GenerateDeviceID()
	if err != nil {
		slog.Warn("generating replacement omemo device id", "protocol", protocol, "jid", s.account.JID, "err", err)
		return 0
	}
	if err := mgr.RotateDeviceID(ctx, next); err != nil {
		slog.Warn("rotating out-of-range omemo device id", "protocol", protocol, "jid", s.account.JID,
			"old_device", old, "new_device", next, "err", err)
		return 0
	}
	slog.Warn("rotated omemo device id out of the unaddressable uint32 range; peers will rebuild sessions",
		"protocol", protocol, "jid", s.account.JID, "old_device", old, "new_device", next)
	return old
}

// deleteOmemoBundle removes the bundle node of a device ID we stopped using.
// Best-effort.
func deleteOmemoBundle(ctx context.Context, client *xmpp.Client, protocol omemolib.Protocol, id omemolib.DeviceID) {
	var err error
	if protocol == omemolib.ProtocolV1 {
		err = client.DeleteOmemoBundleV1(ctx, id)
	} else {
		err = client.DeleteOmemoBundle(ctx, id)
	}
	if err != nil {
		slog.Warn("deleting abandoned omemo bundle node", "protocol", protocol, "device", id, "err", err)
		return
	}
	slog.Debug("deleted abandoned omemo bundle node", "protocol", protocol, "device", id)
}

// pruneOwnDeviceList drops from our own device list rotatedFrom and any
// out-of-range ID (see rotateOutOfRangeDeviceID): one such ID makes our
// messages unreadable for libsignal-based peers. Such a device re-numbers
// itself when it next connects. local is always kept, and reported.
func pruneOwnDeviceList(
	devices []omemolib.DeviceID, local, rotatedFrom omemolib.DeviceID,
	protocol omemolib.Protocol, accountJID string,
) (kept []omemolib.DeviceID, hasLocal bool) {
	kept = make([]omemolib.DeviceID, 0, len(devices)+1)
	for _, id := range devices {
		if id == local {
			hasLocal = true
			kept = append(kept, id)
			continue
		}
		if rotatedFrom != 0 && id == rotatedFrom {
			continue
		}
		if !id.Valid() {
			slog.Warn("omemo setup: dropping unaddressable device from our own published device list",
				"protocol", protocol, "jid", accountJID, "device", id)
			continue
		}
		kept = append(kept, id)
	}
	return kept, hasLocal
}
