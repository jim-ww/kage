package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jim-ww/kage/crypto/localstore"
	kageomemo "github.com/jim-ww/kage/crypto/omemo"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// omemoPeerProtocolTTL bounds how long a cached peer-protocol negotiation
// result (storage.omemoPeerProtocol) is trusted before resolveOmemoProtocol
// re-probes - a peer might upgrade clients between conversations.
const omemoPeerProtocolTTL = 7 * 24 * time.Hour

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
// identities, builds a Manager per protocol against the just-dialed
// client's Transport (PEP bundle/device-list exchange), and publishes both
// bundles and device IDs so contacts can start sessions with us regardless
// of which protocol version they speak. Trust is TOFU: any device we
// haven't seen before is trusted on first use, matching gpg's
// --trust-model always. Best-effort like publishOwnGPGKey — a failure here
// just means omemo-mode chats fall back to sending unencrypted (see
// resolveEncryptionMode/send).
func setupOmemo(ctx context.Context, s *accountSession) {
	client := s.client.Load()

	s.omemoMgrV2 = setupOmemoProtocol(ctx, s, client, omemolib.ProtocolV2, client.OmemoTransport(),
		client.FetchOmemoDeviceList, client.PublishOmemoDeviceList)
	s.omemoMgrV1 = setupOmemoProtocol(ctx, s, client, omemolib.ProtocolV1, client.OmemoTransportV1(),
		client.FetchOmemoDeviceListV1, client.PublishOmemoDeviceListV1)
}

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
		// sql.ErrNoRows means exactly one thing: no identity has been
		// generated for this (account, protocol) yet, which is the expected
		// state on first run - go generate one below. Any other error is the
		// local sqlite database itself misbehaving (a schema mismatch, a
		// corrupt file, disk failure, ...); silently falling back to
		// "encryption unavailable" here would mean a chat configured for
		// OMEMO silently sends plaintext instead - unacceptable, so this is
		// fatal instead of a warning.
		if !errors.Is(err, sql.ErrNoRows) {
			log.Fatalf("fatal: loading omemo(%s) identity for %s: %v", protocol, s.account.JID, err)
		}

		var deviceID [4]byte
		if _, err := rand.Read(deviceID[:]); err != nil {
			debugf("warning: generating omemo(%s) device id for %s: %v\n", protocol, s.account.JID, err)
			return nil
		}
		id := omemolib.DeviceID(binary.BigEndian.Uint32(deviceID[:]))
		if err := omemolib.InitIdentity(ctx, store, s.account.JID, id, protocol); err != nil {
			// Same reasoning as above: the row we just proved absent should
			// always insert cleanly against a correct schema, so a failure
			// here is the database itself being broken, not a recoverable
			// omemo-level condition.
			log.Fatalf("fatal: initializing omemo(%s) identity for %s: %v", protocol, s.account.JID, err)
		}
	}

	mgr, err := omemolib.NewManager(ctx, store, transport, protocol,
		omemolib.WithTrustResolver(func(context.Context, omemolib.Device, []byte) error { return nil }))
	if err != nil {
		debugf("warning: setting up omemo(%s) manager for %s: %v\n", protocol, s.account.JID, err)
		return nil
	}

	if err := mgr.PublishBundle(ctx); err != nil {
		debugf("warning: publishing omemo(%s) bundle for %s: %v\n", protocol, s.account.JID, err)
	}

	devices, err := fetchDeviceList(ctx, s.account.JID)
	if err != nil {
		debugf("warning: fetching own omemo(%s) device list for %s: %v\n", protocol, s.account.JID, err)
		return mgr
	}
	local := mgr.LocalDevice().ID
	debugf("omemo(%s) setup: local device ID for %s: %d, current device list: %v", protocol, s.account.JID, local, devices.Devices)
	alreadyListed := false
	for _, id := range devices.Devices {
		if id == local {
			alreadyListed = true
			break
		}
	}
	// Republish even when already listed, not just on first-ever creation:
	// this is the only place that reconfigures the device-list PEP node's
	// access model to "open" (via publishDeviceList -> makeNodeOpen). If that
	// reconfiguration silently failed the one time the device was first
	// added, skipping republish here left the node stuck on its restrictive
	// default forever, invisible to peers who aren't presence-subscribed
	// just right, with nothing in our own logs to show it.
	devices.JID = s.account.JID
	if !alreadyListed {
		devices.Devices = append(devices.Devices, local)
	}
	debugf("omemo(%s) setup: publishing device list for %s with devices: %v", protocol, s.account.JID, devices.Devices)
	if err := publishDeviceList(ctx, devices); err != nil {
		debugf("warning: publishing omemo(%s) device list for %s: %v\n", protocol, s.account.JID, err)
	}
	return mgr
}

// omemoManagerFor returns s's Manager for protocol, or nil if that
// protocol's setup hasn't completed (or failed) for this account.
func (s *accountSession) omemoManagerFor(protocol omemolib.Protocol) *omemolib.Manager {
	if protocol == omemolib.ProtocolV1 {
		return s.omemoMgrV1
	}
	return s.omemoMgrV2
}

// resolveEncryptionMode returns the outgoing message encryption mode for
// peerJID: "omemo-auto" (the default - auto-detects the peer's protocol
// version, see resolveOmemoProtocol), "omemo-v1"/"omemo-v2" (force that
// protocol regardless of auto-detection), "gpg", or "none" if the user
// explicitly disabled encryption for this chat — see
// ui.ChatEncryptionSetter/ui.encryptionModes.
func resolveEncryptionMode(ctx context.Context, s *accountSession, peerJID string) string {
	mode, err := s.db.GetChatEncryptionMode(ctx, storage.GetChatEncryptionModeParams{
		AccountJid: s.account.JID,
		RosterJid:  peerJID,
	})
	if err != nil || mode == "" {
		return "omemo-auto"
	}
	return mode
}

// resolveOmemoProtocol decides which OMEMO protocol to use for peerJID:
// config's omemo_peers override always wins; otherwise a cached negotiation
// result (storage.omemoPeerProtocol) is reused within omemoPeerProtocolTTL;
// otherwise both protocols' device-list PEP nodes are probed and whichever
// responds with a non-empty list wins (v2 preferred if both do), and the
// result is cached. Defaults to ProtocolV2 if neither responds.
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
		debugf("warning: caching omemo protocol negotiation for %s: %v\n", peerJID, err)
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

// resolveOmemoManagerForMode resolves which OMEMO protocol/Manager to use
// for peerJID given mode ("omemo-auto", "omemo-v1", or "omemo-v2" — any
// other mode is a caller error). "omemo-v1"/"omemo-v2" force that protocol
// directly, bypassing resolveOmemoProtocol's auto-detection entirely - for
// contacts whose client doesn't advertise correctly but is known to only
// support one version.
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
