package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"path/filepath"
	"sync"
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

// encryptForStorage seals plaintext under the shared local storage key if
// one is configured; with no key (no local storage password set) the body
// is stored as-is, and encrypted comes back false.
func encryptForStorage(s *accountSession, plaintext string) (body sql.NullString, encrypted bool) {
	if s.localKey == nil {
		return sql.NullString{String: plaintext, Valid: true}, false
	}
	ct, err := localstore.Seal(s.localKey, plaintext)
	if err != nil {
		slog.Warn("encrypting message for storage", "err", err)
		return sql.NullString{String: plaintext, Valid: true}, false
	}
	return sql.NullString{String: ct, Valid: true}, true
}

// loadDraft returns row's plaintext draft body, decrypting it with localKey
// (crypto/localstore) if it's stored encrypted. A plaintext row read while a
// password *is* now available gets opportunistically re-sealed and written
// back — same migration readStoredBody does for message bodies — so a draft
// saved before a storage password was ever configured gets swept up the
// next time it's loaded rather than sitting in plaintext forever. If a
// decrypt is required but impossible (no key, or the key doesn't open it),
// best-effort: yields an empty draft with a warning logged, rather than
// failing whatever's loading the chat list.
func loadDraft(ctx context.Context, queries *storage.Queries, accountJID string, row storage.ListChatDraftsRow, localKey []byte) string {
	if !row.Encrypted {
		if localKey != nil {
			if ct, err := localstore.Seal(localKey, row.Body); err == nil {
				if err := queries.SetChatDraft(ctx, storage.SetChatDraftParams{
					AccountJid: accountJID, RosterJid: row.Rosterjid, Body: ct, Encrypted: true,
				}); err != nil {
					slog.Warn("encrypting stored draft", "err", err)
				}
			} else {
				slog.Warn("encrypting stored draft", "err", err)
			}
		}
		return row.Body
	}
	if localKey == nil {
		slog.Warn("stored draft is encrypted but no local storage password is available")
		return ""
	}
	pt, err := localstore.Open(localKey, row.Body)
	if err != nil {
		slog.Warn("decrypting stored draft", "err", err)
		return ""
	}
	return pt
}

// decryptOutboxBody returns row's plaintext body, decrypting it with
// localKey (crypto/localstore) if it's stored encrypted - the outbox
// table's counterpart to loadDraft, for the same reason (best-effort:
// yields empty on failure rather than blocking startup/flush).
func decryptOutboxBody(row storage.Outbox, localKey []byte) string {
	if !row.Encrypted {
		return row.Body
	}
	if localKey == nil {
		slog.Warn("stored outbox entry is encrypted but no local storage password is available", "to", row.Toattr)
		return ""
	}
	pt, err := localstore.Open(localKey, row.Body)
	if err != nil {
		slog.Warn("decrypting stored outbox entry", "err", err)
		return ""
	}
	return pt
}

// outboxRowToMessage converts a stored outbox row for a plain new-message
// send or a staged-attachment send (not a reaction/retraction/correction -
// see pendingOutboxMessagesByPeer, which filters to only those before
// calling this) into a Pending ui.Message for chat history - the DB-backed
// counterpart of sendCurrentInput's/startAttachedSend's local echo, shown
// identically (Pending, no ID, no Encrypted marking yet) until
// MessageSendResolvedMsg reports what actually happened to it. A
// staged-attachment row's real URL isn't known yet (the upload itself
// hasn't happened) - shown as the caption text plus the local file's name
// so there's still something recognizable on screen instead of nothing.
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

// publishOwnGPGKey exports our own public key and publishes it to our PEP
// nodes (XEP-0373) so contacts can discover it automatically instead of us
// having to hand them a fingerprint out of band. Best-effort: some servers
// don't support PEP or restrict node creation, so failure here just means
// contacts fall back to a manually configured gpg_peers entry, same as
// before this existed.
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

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.omemoMgrV2 = setupOmemoProtocol(ctx, s, client, omemolib.ProtocolV2, client.OmemoTransport(),
			client.FetchOmemoDeviceList, client.PublishOmemoDeviceList)
	}()
	go func() {
		defer wg.Done()
		s.omemoMgrV1 = setupOmemoProtocol(ctx, s, client, omemolib.ProtocolV1, client.OmemoTransportV1(),
			client.FetchOmemoDeviceListV1, client.PublishOmemoDeviceListV1)
	}()
	wg.Wait()
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
			slog.Warn("generating omemo device id", "protocol", protocol, "jid", s.account.JID, "err", err)
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
		slog.Warn("setting up omemo manager", "protocol", protocol, "jid", s.account.JID, "err", err)
		return nil
	}

	if err := mgr.PublishBundle(ctx); err != nil {
		slog.Warn("publishing omemo bundle", "protocol", protocol, "jid", s.account.JID, "err", err)
	}

	// One-time prekeys are consumed (deleted from store) the moment any
	// device starts a fresh session with us, and the pool is otherwise only
	// ever seeded once at identity creation - nothing else in kage ever
	// tops it up. Left unchecked, the pool eventually runs dry (every new
	// session then fails outright), and even before that, any peer holding
	// a bundle snapshot fetched before a prekey was consumed will try to
	// use an ID we've already deleted, failing with "no rows in result
	// set". Checking and republishing here, on every connect, keeps the
	// published bundle honest and the pool from ever draining permanently.
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
	slog.Debug("omemo setup: publishing device list", "protocol", protocol, "jid", s.account.JID, "devices", devices.Devices)
	if err := publishDeviceList(ctx, devices); err != nil {
		slog.Warn("publishing omemo device list", "protocol", protocol, "jid", s.account.JID, "err", err)
	}

	// The fetch above went straight through the raw transport (only to
	// decide whether this device needed appending/republishing) and never
	// touched mgr's own device cache - the one EncryptMessage's
	// recipientDevices actually reads to know which of our other devices
	// to include. Without this, a device another of our own clients
	// added/rotated while we were offline stays invisible to us until a
	// live DeviceListChangedEvent push happens to arrive post-connect (it
	// won't, if nothing else changes the list again), so messages we send
	// would silently never carry a key for it. resyncPeerDeviceLists
	// (account.go) does this same SyncDevices call for every roster peer
	// on connect; this is that same treatment for our own JID, which isn't
	// a roster entry so that loop never reaches it.
	if err := mgr.SyncDevices(ctx, s.account.JID); err != nil {
		slog.Warn("resyncing own omemo device list on connect", "protocol", protocol, "jid", s.account.JID, "err", err)
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
// peerJID: "omemo-v1" (the default), "omemo-v2" (force that protocol),
// "gpg", or "none" if the user explicitly disabled encryption for this
// chat — see ui.ChatEncryptionSetter/ui.encryptionModes.
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

// resolveOmemoManagerForMode resolves which OMEMO protocol/Manager to use
// for peerJID given mode ("omemo-v1" or "omemo-v2" — any other mode is a
// caller error, and falls back to auto-detection via resolveOmemoProtocol
// for legacy stored modes such as the removed "omemo-auto").
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
