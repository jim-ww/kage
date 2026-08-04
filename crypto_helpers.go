package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"

	"github.com/jim-ww/kage/crypto/localstore"
	kageomemo "github.com/jim-ww/kage/crypto/omemo"
	"github.com/jim-ww/kage/storage"
	omemolib "github.com/jim-ww/omemo-go"
)

// encryptForStorage seals plaintext under the shared local storage key if
// one is configured; with no key (no local storage password set) the body
// is stored as-is, and encrypted comes back false.
func encryptForStorage(s *accountSession, plaintext string) (body sql.NullString, encrypted bool) {
	if s.localKey == nil {
		return sql.NullString{String: plaintext, Valid: true}, false
	}
	ct, err := localstore.Seal(s.localKey, plaintext)
	if err != nil {
		debugf("warning: encrypting message for storage: %v\n", err)
		return sql.NullString{String: plaintext, Valid: true}, false
	}
	return sql.NullString{String: ct, Valid: true}, true
}

// publishOwnGPGKey exports our own public key and publishes it to our PEP
// nodes (XEP-0373) so contacts can discover it automatically instead of us
// having to hand them a fingerprint out of band. Best-effort: some servers
// don't support PEP or restrict node creation, so failure here just means
// contacts fall back to a manually configured gpg_peers entry, same as
// before this existed.
func publishOwnGPGKey(ctx context.Context, s *accountSession) {
	keyData, err := s.gpg.Export(s.account.GPGKeyID)
	if err != nil {
		debugf("warning: exporting own gpg key: %v\n", err)
		return
	}
	if err := s.client.Load().PublishOpenPGPKey(ctx, s.account.GPGKeyID, keyData); err != nil {
		debugf("warning: publishing gpg key via PEP (XEP-0373): %v\n", err)
		return
	}
}

// setupOmemo loads (or, on first run, generates) this account's OMEMO
// identity, builds its Manager against the just-dialed client's Transport
// (XEP-0384 PEP bundle/device-list exchange), and publishes our bundle and
// device ID so contacts can start sessions with us. Trust is TOFU: any
// device we haven't seen before is trusted on first use, matching gpg's
// --trust-model always. Best-effort like publishOwnGPGKey — a failure here
// just means omemo-mode chats fall back to sending unencrypted (see
// resolveEncryptionMode/send).
func setupOmemo(ctx context.Context, s *accountSession) {
	store := kageomemo.NewStore(s.db, s.account.JID)

	if _, err := store.IdentityKeyPair(ctx); err != nil {
		var deviceID [4]byte
		if _, err := rand.Read(deviceID[:]); err != nil {
			debugf("warning: generating omemo device id for %s: %v\n", s.account.JID, err)
			return
		}
		id := omemolib.DeviceID(binary.BigEndian.Uint32(deviceID[:]))
		if err := omemolib.InitIdentity(ctx, store, s.account.JID, id); err != nil {
			debugf("warning: initializing omemo identity for %s: %v\n", s.account.JID, err)
			return
		}
	}

	client := s.client.Load()
	mgr, err := omemolib.NewManager(ctx, store, client.OmemoTransport(),
		omemolib.WithTrustResolver(func(context.Context, omemolib.Device, ed25519.PublicKey) error { return nil }))
	if err != nil {
		debugf("warning: setting up omemo for %s: %v\n", s.account.JID, err)
		return
	}
	s.omemoMgr = mgr

	if err := mgr.PublishBundle(ctx); err != nil {
		debugf("warning: publishing omemo bundle for %s: %v\n", s.account.JID, err)
	}

	devices, err := client.FetchOmemoDeviceList(ctx, s.account.JID)
	if err != nil {
		debugf("warning: fetching own omemo device list for %s: %v\n", s.account.JID, err)
		return
	}
	local := mgr.LocalDevice().ID
	debugf("omemo setup: local device ID for %s: %d, current device list: %v", s.account.JID, local, devices.Devices)
	for _, id := range devices.Devices {
		if id == local {
			debugf("omemo setup: device %d already in list for %s", local, s.account.JID)
			return // already listed
		}
	}
	devices.JID = s.account.JID
	devices.Devices = append(devices.Devices, local)
	debugf("omemo setup: publishing device list for %s with devices: %v", s.account.JID, devices.Devices)
	if err := client.PublishOmemoDeviceList(ctx, devices); err != nil {
		debugf("warning: publishing omemo device list for %s: %v\n", s.account.JID, err)
	}
}

// resolveEncryptionMode returns the outgoing message encryption mode for
// peerJID ("omemo" the default, "gpg", or "none" if the user explicitly
// disabled encryption for this chat — see ui.ChatEncryptionSetter).
func resolveEncryptionMode(ctx context.Context, s *accountSession, peerJID string) string {
	mode, err := s.db.GetChatEncryptionMode(ctx, storage.GetChatEncryptionModeParams{
		AccountJid: s.account.JID,
		RosterJid:  peerJID,
	})
	if err != nil {
		return "omemo"
	}
	return mode
}

// resolvePeerKey returns the GPG key fingerprint to encrypt to-messages with
// for peerJID, or "" if none is available. Order: an explicit gpg_peers
// override in config always wins (lets a user pin a specific key); then a
// previously discovered-and-cached fingerprint; then a live XEP-0373 PEP
// lookup, which gets cached in storage so it's a one-time cost per contact.
func resolvePeerKey(ctx context.Context, s *accountSession, peerJID string) string {
	if key := s.account.GPGPeers[peerJID]; key != "" {
		return key
	}

	if fpr, err := s.db.GetPGPPeerKey(ctx, storage.GetPGPPeerKeyParams{AccountJid: s.account.JID, Jid: peerJID}); err == nil && fpr != "" {
		return fpr
	}

	fpr, err := discoverPeerKey(ctx, s, peerJID)
	if err != nil {
		debugf("note: no gpg key found for %s (%v); sending unencrypted\n", peerJID, err)
		return ""
	}
	if err := s.db.UpsertPGPPeerKey(ctx, storage.UpsertPGPPeerKeyParams{AccountJid: s.account.JID, Jid: peerJID, Fingerprint: fpr}); err != nil {
		debugf("warning: caching discovered gpg key for %s: %v\n", peerJID, err)
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
