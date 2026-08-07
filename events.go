package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/daemon"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// handleIncomingMessage decrypts, persists, and forwards a single incoming
// chat message — routing XEP-0308 corrections to storage.UpdateMessageBodyByID
// and a MessageCorrectedMsg instead of appending a new message, and XEP-0424
// retractions to a flag (never an actual delete — see ui.Message.Retracted).
func handleIncomingMessage(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession, msgEv xmpp.MessageEvent) {
	if msgEv.ReactionTargetID != "" {
		from := bareJID(msgEv.From)
		if err := replaceReactions(ctx, s, from, msgEv.ReactionTargetID, from, msgEv.Reactions); err != nil {
			slog.Warn("persisting reactions", "from", from, "err", err)
		}
		broadcast(srv, evMessageReactions, ui.MessageReactionsMsg{
			AccountIdx: accountIdx,
			From:       from,
			MessageID:  msgEv.ReactionTargetID,
			Reactions:  loadReactionsForMessage(ctx, s, from, msgEv.ReactionTargetID),
		})
		return
	}

	if msgEv.RetractID != "" {
		from := bareJID(msgEv.From)
		if _, err := s.db.MarkMessageRetracted(ctx, storage.MarkMessageRetractedParams{
			AccountJid: s.account.JID,
			IDAttr:     nullString(msgEv.RetractID),
			RosterJid:  nullString(from),
		}); err != nil {
			slog.Warn("persisting retraction flag", "err", err)
		}
		broadcast(srv, evMessageRetracted, ui.MessageRetractedMsg{
			AccountIdx: accountIdx,
			From:       from,
			RetractID:  msgEv.RetractID,
		})
		return
	}

	body := msgEv.Body
	oobURLs := msgEv.OOBURLs
	e2eEncrypted := msgEv.Encrypted != nil || msgEv.EncryptedV1 != nil || gpg.Looks(body)
	e2eeMethod := ""
	switch {
	case msgEv.Encrypted != nil:
		e2eeMethod = "omemo-v2"
	case msgEv.EncryptedV1 != nil:
		e2eeMethod = "omemo-v1"
	case gpg.Looks(body):
		e2eeMethod = "gpg"
	}
	decryptFailed := false

	// Held across the OMEMO decrypt-or-skip decision through InsertMessage
	// below: serializes against syncArchiveForContact's processMAMItem
	// (account.go), which decrypts/persists over the same OMEMO state in a
	// separate goroutine - see accountSession.omemoMu's doc comment for what
	// goes wrong without this.
	s.omemoMu.Lock()
	defer s.omemoMu.Unlock()

	if msgEv.Encrypted != nil || msgEv.EncryptedV1 != nil {
		// A message already backfilled via MAM (or otherwise already stored -
		// e.g. redelivered after a reconnect) would otherwise get OMEMO
		// decrypt re-run on it here: wasteful at best, and for a message key
		// already consumed out of the double ratchet's skip buffer, not
		// guaranteed to fail cleanly the second time. Check storage first.
		from := bareJID(msgEv.From)
		if msgEv.ID != "" {
			if exists, err := s.db.MessageExistsByIDAttr(ctx, storage.MessageExistsByIDAttrParams{
				AccountJid: s.account.JID,
				RosterJid:  nullString(from),
				IDAttr:     nullString(msgEv.ID),
			}); err == nil && exists {
				slog.Debug("omemo message already stored, skipping re-decrypt", "id", msgEv.ID, "from", msgEv.From)
				return
			}
		}

		slog.Debug("received omemo message", "from", msgEv.From, "jid", s.account.JID)

		var mgr *omemolib.Manager
		var enc *omemolib.EncryptedMessage
		var err error
		if msgEv.Encrypted != nil {
			mgr = s.omemoMgrV2
			enc, err = xmpp.DecodeOmemoMessage(msgEv.Encrypted, bareJID(msgEv.From))
		} else {
			mgr = s.omemoMgrV1
			enc, err = xmpp.DecodeOmemoMessageV1(msgEv.EncryptedV1, bareJID(msgEv.From))
		}
		if err != nil {
			slog.Warn("decoding omemo message", "from", msgEv.From, "err", err)
			return
		}
		if mgr == nil {
			slog.Warn("received omemo message but omemo isn't ready", "from", msgEv.From, "jid", s.account.JID)
			return
		}
		pt, err := mgr.DecryptMessage(ctx, enc)
		if errors.Is(err, omemolib.ErrOwnDeviceKeyMissing) {
			// Not a real failure - this stanza just wasn't encrypted for this
			// device's current key (e.g. it also targets other/older devices
			// on the same account). Nothing was lost, so stay quiet instead
			// of cluttering the chat with a per-device non-error.
			slog.Debug("omemo message has no key for this device, skipping", "from", msgEv.From)
			return
		} else if err != nil {
			// Surface the failure in the chat instead of dropping the message
			// entirely - a silently vanished message looks indistinguishable
			// from "nothing was sent", which makes this undiagnosable from the UI.
			slog.Warn("decrypting omemo message failed", "from", msgEv.From, "err", err)
			body = "[message could not be decrypted: " + err.Error() + "]"
			decryptFailed = true
			if errors.Is(err, omemolib.ErrUnknownSession) {
				// The message itself is unrecoverable (a plain ratchet
				// message can't bootstrap a session), but nothing stops
				// every future message from sender's device failing the
				// same way forever, since sender has no way to learn their
				// session with us is broken. Rebuild one now and push it to
				// them - mirrors Conversations' AxolotlService "healing"
				// (buildSessionFromPEP + a key-transport reply) - so this is
				// the last message lost to it, not every one from here on.
				healBrokenSession(ctx, s, mgr, enc.Sender, from)
			}
		} else if pt == nil {
			slog.Debug("omemo message was key-transport only (no content)", "from", msgEv.From)
			return // key-transport message: session established/refreshed, no content to show
		} else {
			slog.Debug("omemo message decrypted successfully", "from", msgEv.From)
			body = string(pt)
		}
	}
	if s.useGPG && gpg.Looks(body) {
		pt, err := s.gpg.Decrypt(body, s.account.GPGPeers[msgEv.From])
		if err != nil {
			slog.Warn("decrypting message", "from", msgEv.From, "err", err)
		} else {
			body = pt
		}
	}
	// Encrypted replies carry their reply-quote in-band (adapter.go's send()
	// prepends "> ..." lines to the plaintext before encrypting, since a
	// wire-level XEP-0461 <fallback> range can't mark up ciphertext) - strip
	// it back off now that we've decrypted, mirroring what the unencrypted
	// path already gets for free from the <fallback> element (see
	// xmpp/events.go's Body doc comment). Applied unconditionally rather than gated
	// on msgEv.ReplyToID: some peers (e.g. this doesn't always round-trip
	// through our own <reply/> parsing) still send an in-band quote without
	// a recognized reply element, and stripReplyQuote is a no-op on a body
	// that doesn't start with one anyway.
	body = stripReplyQuote(body)
	if len(oobURLs) == 0 {
		oobURLs = aesgcmURLsInBody(body)
	}

	from := bareJID(msgEv.From) // chats are keyed by bare JID (roster entries); From with a resource never matches

	if msgEv.ReplaceID != "" {
		sealedBody, encrypted := encryptForStorage(s, body)
		if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
			AccountJid:   s.account.JID,
			Body:         sealedBody,
			Encrypted:    encrypted,
			E2eEncrypted: e2eEncrypted,
			E2eeMethod:   nullString(e2eeMethod),
			Edited:       true,
			IDAttr:       nullString(msgEv.ReplaceID),
			RosterJid:    nullString(from),
		}); err != nil {
			slog.Warn("persisting correction", "err", err)
		}
		broadcast(srv, evMessageCorrected, ui.MessageCorrectedMsg{
			AccountIdx: accountIdx,
			From:       from,
			ReplaceID:  msgEv.ReplaceID,
			NewContent: body,
			Encrypted:  e2eEncrypted,
			EncMethod:  e2eeMethod,
		})
		return
	}

	// A redelivered stanza (e.g. after stream resumption) can reach here with
	// an idAttr already stored - the OMEMO branch above checks this before
	// decrypting for its own reason (ratchet safety), but a plaintext message
	// needs the same check for InsertMessage's sake: without it, the unique
	// index below rejects the row but the message still gets sent to the UI,
	// producing a visible duplicate that vanishes on next reload.
	if msgEv.ID != "" {
		if exists, err := s.db.MessageExistsByIDAttr(ctx, storage.MessageExistsByIDAttrParams{
			AccountJid: s.account.JID,
			RosterJid:  nullString(from),
			IDAttr:     nullString(msgEv.ID),
		}); err == nil && exists {
			slog.Debug("message already stored, skipping duplicate", "id", msgEv.ID, "from", msgEv.From)
			return
		}
	}

	sealedBody, encrypted := encryptForStorage(s, body)
	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid:    s.account.JID,
		Sent:          false,
		FromAttr:      nullString(msgEv.From),
		IDAttr:        nullString(msgEv.ID),
		Body:          sealedBody,
		Encrypted:     encrypted,
		E2eEncrypted:  e2eEncrypted,
		E2eeMethod:    nullString(e2eeMethod),
		StanzaType:    "chat",
		RosterJid:     nullString(from),
		ReplyToIDAttr: nullString(msgEv.ReplyToID),
		OobUrls:       joinOOBURLs(oobURLs),
	}); err != nil {
		// Insertion failed (most likely the unique index rejecting a
		// duplicate idAttr that slipped past the check above in a race) -
		// don't forward it to the UI, or a message with no corresponding
		// stored row appears in chat and then vanishes on next reload.
		slog.Warn("persisting received message", "err", err)
		return
	}

	broadcast(srv, evIncomingMessage, ui.IncomingMessageMsg{
		AccountIdx: accountIdx,
		From:       from,
		ReplyToID:  msgEv.ReplyToID,
		Message: ui.Message{
			ID:            msgEv.ID,
			Author:        s.rosterName(from),
			Content:       body,
			SentAt:        msgEv.SentAt,
			IsMe:          false,
			Encrypted:     e2eEncrypted,
			EncMethod:     e2eeMethod,
			Attachments:   oobURLs,
			DecryptFailed: decryptFailed,
		},
	})

	chatIsFocused := tuiFocused.Load() && tuiActiveChat.Load() == focusedChatKey(s.account.JID, from)
	if notifyEnabled.Load() && !decryptFailed && !chatIsFocused {
		daemon.Notify(s.rosterName(from), notifyPreview(body, oobURLs))
	}
}

// healBrokenSession recovers from an OMEMO decrypt failure caused by a
// broken/missing session with sender (omemolib.ErrUnknownSession) —
// typically our own session state got wiped (e.g. the local database was
// deleted) while sender's client still believes a live session exists, so it
// keeps sending ordinary ratchet messages we can never decrypt instead of
// the prekey message that could bootstrap a fresh session. sender has no
// way to learn this on its own; nothing here fixes that except forcing a
// rebuild ourselves and telling them about it. Best-effort: a failure here
// just means the peer stays broken until something else notices (a restart,
// a device-list push) — no worse than before this existed.
func healBrokenSession(ctx context.Context, s *accountSession, mgr *omemolib.Manager, sender omemolib.Device, peerBareJID string) {
	if err := mgr.ResetSession(ctx, sender); err != nil {
		slog.Warn("healing broken omemo session: clearing stale session", "peer", peerBareJID, "device", sender.ID, "err", err)
		return
	}

	enc, deviceErrs, err := mgr.EncryptKeyTransport(ctx, peerBareJID)
	if err != nil {
		slog.Warn("healing broken omemo session: rebuilding session failed", "peer", peerBareJID, "device", sender.ID, "err", err)
		return
	}
	for _, de := range deviceErrs {
		slog.Debug("healing broken omemo session: one device failed (others still sent)", "peer", peerBareJID, "device_id", de.Device.ID, "err", de.Err)
	}

	client := s.client.Load()
	var sendErr error
	if mgr == s.omemoMgrV1 {
		_, sendErr = client.Send(ctx, peerBareJID, "", xmpp.SendOptions{EncryptedV1: xmpp.EncodeOmemoMessageV1(enc)})
	} else {
		_, sendErr = client.Send(ctx, peerBareJID, "", xmpp.SendOptions{Encrypted: xmpp.EncodeOmemoMessage(enc)})
	}
	if sendErr != nil {
		slog.Warn("healing broken omemo session: sending key-transport reply failed", "peer", peerBareJID, "device", sender.ID, "err", sendErr)
		return
	}
	slog.Debug("healing broken omemo session: rebuilt session and sent key-transport reply", "peer", peerBareJID, "device", sender.ID)
}

// notifyPreview trims body to a reasonable desktop-notification length —
// now that the daemon decrypts before notifying, the previous placeholder
// ("🔒 New encrypted message") is gone; this is real content, so keep it
// short rather than dumping a whole message into the popup. When body is
// (or ends with) an attachment URL, e.g. an OMEMO-encrypted file share whose
// body is just its aesgcm:// link, show the file's normalized filename
// instead of the raw ciphertext-bearing URL.
func notifyPreview(body string, attachments []string) string {
	if len(attachments) > 0 && strings.TrimSpace(body) == strings.Join(attachments, "\n") {
		names := make([]string, len(attachments))
		for i, a := range attachments {
			names[i] = ui.AttachmentDisplayName(a)
		}
		return "📎 " + strings.Join(names, ", ")
	}
	const max = 120
	body = strings.TrimSpace(body)
	if len(body) <= max {
		return body
	}
	return body[:max] + "…"
}

// stripReplyQuote removes the leading "> "-prefixed quote block adapter.go's
// send() bakes into an encrypted reply's plaintext, returning just the
// sender's actual message. A body with no such block (e.g. a message from a
// client that doesn't do in-band quoting) is returned unchanged.
func stripReplyQuote(body string) string {
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) && strings.HasPrefix(lines[i], "> ") {
		i++
	}
	if i == 0 {
		return body
	}
	return strings.Join(lines[i:], "\n")
}

// aesgcmURLsInBody scans body's trailing non-empty lines for aesgcm:// URLs
// (XEP-0454). Unlike a plain https:// link - ambiguous between a real
// upload and the user just pasting a link, which is why plaintext
// attachment detection relies on XEP-0066 OOB alone rather than guessing -
// aesgcm:// is a synthetic scheme nothing but a file share ever produces,
// so finding one isn't a guess, it's a deterministic protocol marker.
// Needed because OMEMO/GPG-encrypted messages can't carry cleartext OOB
// (see xmpp.SendOptions.OOBURLs's doc comment): other clients (e.g.
// Conversations) send encrypted files exactly this way, with no OOB
// element, and so do we.
func aesgcmURLsInBody(body string) []string {
	lines := strings.Split(body, "\n")
	var urls []string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "aesgcm://") {
			break
		}
		urls = append(urls, line)
	}
	slices.Reverse(urls)
	return urls
}

