package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/daemon"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// handleIncomingMessage decrypts, stores and broadcasts an incoming message,
// a correction (XEP-0308) or retraction, which only flags (XEP-0424).
func handleIncomingMessage(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession, msgEv xmpp.MessageEvent) {
	// A "sent" carbon (XEP-0280) is from our own JID; its chat is To.
	peer := bareJID(msgEv.From)
	if msgEv.Outgoing {
		peer = bareJID(msgEv.To)
	}

	if msgEv.ReactionTargetID != "" {
		from := peer
		if err := replaceReactions(ctx, s, from, msgEv.ReactionTargetID, from, msgEv.Reactions); err != nil {
			slog.Warn("persisting reactions", "from", from, "err", err)
		}
		broadcast(srv, ui.MessageReactionsMsg{
			AccountIdx: accountIdx,
			From:       from,
			MessageID:  msgEv.ReactionTargetID,
			Reactions:  loadReactionsForMessage(ctx, s, from, msgEv.ReactionTargetID),
		})
		return
	}

	if msgEv.RetractID != "" {
		from := peer
		if _, err := s.db.MarkMessageRetracted(ctx, storage.MarkMessageRetractedParams{
			AccountJid: s.account.JID,
			IDAttr:     nullString(msgEv.RetractID),
			RosterJid:  nullString(from),
		}); err != nil {
			slog.Warn("persisting retraction flag", "err", err)
		}
		broadcast(srv, ui.MessageRetractedMsg{
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

	// From the decrypt to the insert, see accountSession.omemoMu.
	s.omemoMu.Lock()
	defer s.omemoMu.Unlock()

	if msgEv.Encrypted != nil || msgEv.EncryptedV1 != nil {
		// A stored message can't be decrypted again, its key is consumed.
		if msgEv.ID != "" {
			if exists, err := s.db.MessageExistsByIDAttr(ctx, storage.MessageExistsByIDAttrParams{
				AccountJid: s.account.JID,
				RosterJid:  nullString(peer),
				IDAttr:     nullString(msgEv.ID),
			}); err == nil && exists {
				slog.Debug("omemo message already stored, skipping re-decrypt", "id", msgEv.ID, "from", msgEv.From)
				return
			}
			// Nor an applied correction, which has no row (see
			// appliedCorrections in schema.sql).
			if msgEv.ReplaceID != "" {
				if exists, err := s.db.CorrectionAppliedByIDAttr(ctx, storage.CorrectionAppliedByIDAttrParams{
					AccountJid: s.account.JID,
					RosterJid:  peer,
					IDAttr:     nullString(msgEv.ID),
				}); err == nil && exists {
					slog.Debug("correction already applied, skipping re-decrypt", "id", msgEv.ID, "from", msgEv.From)
					return
				}
			}
		}

		slog.Debug("received omemo message", "from", msgEv.From, "jid", s.account.JID)

		var mgr *omemolib.Manager
		var protocol omemolib.Protocol
		var enc *omemolib.EncryptedMessage
		var err error
		if msgEv.Encrypted != nil {
			mgr, protocol = s.omemoV2(), omemolib.ProtocolV2
			enc, err = xmpp.DecodeOmemoMessage(msgEv.Encrypted, bareJID(msgEv.From))
		} else {
			mgr, protocol = s.omemoV1(), omemolib.ProtocolV1
			enc, err = xmpp.DecodeOmemoMessageV1(msgEv.EncryptedV1, bareJID(msgEv.From))
		}
		if err != nil {
			slog.Warn("decoding omemo message", "from", msgEv.From, "err", err)
			return
		}
		if mgr == nil {
			// Usually a message arriving before setupOmemo finished.
			s.waitOmemoReady(ctx, omemoSetupGrace)
			mgr = s.omemoManagerFor(protocol)
		}
		if mgr == nil {
			// Stored as undecryptable, not dropped.
			slog.Warn("received omemo message but omemo isn't ready", "from", msgEv.From, "jid", s.account.JID)
			body = "[message could not be decrypted: omemo isn't ready]"
			decryptFailed = true
		} else if pt, err := mgr.DecryptMessage(ctx, enc); errors.Is(err, omemolib.ErrOwnDeviceKeyMissing) {
			// Not for this device: nothing lost.
			slog.Debug("omemo message has no key for this device, skipping", "from", msgEv.From)
			return
		} else if err != nil {
			// Surface the failure in the chat instead of dropping the message
			// entirely - a silently vanished message looks indistinguishable
			// from "nothing was sent", which makes this undiagnosable from the UI.
			slog.Warn("decrypting omemo message failed", "from", msgEv.From, "err", err)
			body = "[message could not be decrypted: " + err.Error() + "]"
			decryptFailed = true
			if errors.Is(err, omemolib.ErrUnknownSession) || errors.Is(err, omemolib.ErrPreKeyNotFound) {
				// This message is lost, but the sender would keep sending on
				// the broken session: rebuild it, as Conversations does.
				healBrokenSession(ctx, s, mgr, protocol, enc.Sender, bareJID(msgEv.From))
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
	// An encrypted reply carries its quote in the text, which a <fallback>
	// can't mark up in ciphertext. Stripped even without a reply element:
	// some clients send the quote alone.
	body = stripReplyQuote(body)
	if len(oobURLs) == 0 {
		oobURLs = aesgcmURLsInBody(body)
	}

	from := peer // chats are keyed by bare JID (roster entries); From with a resource never matches

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
		if msgEv.ID != "" {
			if err := s.db.InsertAppliedCorrection(ctx, storage.InsertAppliedCorrectionParams{
				AccountJid: s.account.JID,
				RosterJid:  from,
				IDAttr:     nullString(msgEv.ID),
			}); err != nil {
				slog.Warn("recording applied correction", "err", err)
			}
		}
		broadcast(srv, ui.MessageCorrectedMsg{
			AccountIdx: accountIdx,
			From:       from,
			ReplaceID:  msgEv.ReplaceID,
			NewContent: body,
			Encrypted:  e2eEncrypted,
			EncMethod:  e2eeMethod,
		})
		return
	}

	// A redelivered message is stored already; broadcasting it again would
	// show a duplicate.
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

	chatName := s.rosterName(from)
	quotedAuthor, quotedPreview := resolveReplyQuote(ctx, s, from, chatName, msgEv.ReplyToID)

	// Before the insert: ChatAddedMsg carries the stored history, and this
	// message follows as IncomingMessageMsg.
	ensureChat(ctx, srv, accountIdx, s, from)

	sealedBody, encrypted := encryptForStorage(s, body)
	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid:        s.account.JID,
		Sent:              msgEv.Outgoing,
		FromAttr:          nullString(msgEv.From),
		IDAttr:            nullString(msgEv.ID),
		Body:              sealedBody,
		Encrypted:         encrypted,
		E2eEncrypted:      e2eEncrypted,
		E2eeMethod:        nullString(e2eeMethod),
		StanzaType:        "chat",
		RosterJid:         nullString(from),
		ReplyToIDAttr:     nullString(msgEv.ReplyToID),
		ReplyQuoteAuthor:  nullString(quotedAuthor),
		ReplyQuotePreview: nullString(quotedPreview),
		OobUrls:           joinOOBURLs(oobURLs),
	}); err != nil {
		// Not broadcast without a stored row.
		slog.Warn("persisting received message", "err", err)
		return
	}

	newMsg := ui.Message{
		ID:            msgEv.ID,
		Author:        chatName,
		Content:       body,
		SentAt:        msgEv.SentAt,
		IsMe:          msgEv.Outgoing,
		Encrypted:     e2eEncrypted,
		EncMethod:     e2eeMethod,
		Attachments:   oobURLs,
		DecryptFailed: decryptFailed,
		QuotedAuthor:  quotedAuthor,
		QuotedPreview: quotedPreview,
	}
	broadcast(srv, ui.IncomingMessageMsg{
		AccountIdx: accountIdx,
		From:       from,
		ReplyToID:  msgEv.ReplyToID,
		Message:    newMsg,
	})
	countUnread(ctx, srv, s, from, []ui.Message{newMsg})

	if shouldNotify(s.account.JID, from, decryptFailed, msgEv.Outgoing) {
		daemon.Notify(s.rosterName(from), notifyPreview(body, oobURLs))
	}
}

// shouldNotify decides whether an incoming message from `from` raises a
// desktop notification: not when notifications are off, not for our own
// message or one we couldn't decrypt, not for a chat someone has on screen,
// and not for a chat the user muted.
func shouldNotify(accountJID, from string, decryptFailed, outgoing bool) bool {
	incomingKey := focusedChatKey(accountJID, from)
	chatIsFocused := clientFocus.chatVisible(incomingKey)
	muted := mutedChats.has(accountJID, from)
	slog.Debug("notify decision", "incomingKey", incomingKey, "clients", clientFocus.describe(), "chatIsFocused", chatIsFocused, "muted", muted, "notifyEnabled", notifyEnabled.Load(), "decryptFailed", decryptFailed, "outgoing", outgoing)
	return notifyEnabled.Load() && !decryptFailed && !chatIsFocused && !muted && !outgoing
}

// healBrokenSession rebuilds a broken OMEMO session with sender, typically
// after we lost ours while sender still uses it, and tells sender with a
// key-transport message: it can't learn otherwise. Best-effort.
func healBrokenSession(ctx context.Context, s *accountSession, mgr *omemolib.Manager, protocol omemolib.Protocol, sender omemolib.Device, peerBareJID string) {
	if err := mgr.ResetSession(ctx, sender); err != nil {
		slog.Warn("healing broken omemo session: clearing stale session", "peer", peerBareJID, "device", sender.ID, "err", err)
		return
	}

	// The key-transport only reaches cached devices: fetch the list first,
	// so sender's device is among them.
	if err := mgr.SyncDevices(ctx, peerBareJID); err != nil {
		slog.Debug("healing broken omemo session: resyncing peer device list failed", "peer", peerBareJID, "device", sender.ID, "err", err)
	}

	enc, deviceErrs, err := mgr.EncryptKeyTransport(ctx, peerBareJID)
	if err != nil {
		slog.Warn("healing broken omemo session: rebuilding session failed", "peer", peerBareJID, "device", sender.ID, "err", err)
		return
	}
	for _, de := range deviceErrs {
		slog.Debug("healing broken omemo session: one device failed (others still sent)", "peer", peerBareJID, "device_id", de.Device.ID, "err", de.Err)
	}
	reachedSender := false
	for _, k := range enc.Keys {
		if k.Device == sender.ID {
			reachedSender = true
			break
		}
	}
	if !reachedSender {
		// The heal missed the one device that needed it.
		slog.Warn("healing broken omemo session: key-transport reply didn't include the broken device (not in known device list)", "peer", peerBareJID, "device", sender.ID)
	}

	client := s.client.Load()
	var sendErr error
	// By protocol, not by comparing mgr: a reconnect can swap managers.
	if protocol == omemolib.ProtocolV1 {
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

// notifyPreview is body cut to notification length, attachment URLs shown
// as file names.
func notifyPreview(body string, attachments []string) string {
	if len(attachments) > 0 && strings.TrimSpace(body) == strings.Join(attachments, "\n") {
		names := make([]string, len(attachments))
		for i, a := range attachments {
			names[i] = ui.AttachmentDisplayName(a)
		}
		return "📎 " + strings.Join(names, ", ")
	}
	const max = 120
	body = ui.StripMessageStyling(strings.TrimSpace(body))
	if len(body) <= max {
		return body
	}
	return body[:max] + "…"
}

// stripReplyQuote removes a reply's leading "> " quote block from body.
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

// aesgcmURLsInBody is the aesgcm:// URLs (XEP-0454) on body's last lines:
// encrypted files carry no XEP-0066 marker, but only files use that scheme,
// unlike an https:// link.
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

// ensureChat gives a peer not in the roster a chat before its message is
// stored: a local roster row, broadcast. Not a server roster item, which
// would ask them for a subscription.
func ensureChat(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession, peer string) {
	if peer == "" || peer == s.account.JID {
		return
	}
	var known bool
	s.mutateRoster(func(entries map[string]rosterEntry) {
		if _, ok := entries[peer]; ok {
			known = true
			return
		}
		entries[peer] = rosterEntry{}
	})
	if known {
		return
	}

	slog.Debug("first message from a contact not in the roster; creating a local chat", "jid", s.account.JID, "contact", peer)
	if err := s.db.UpsertRoster(ctx, storage.UpsertRosterParams{
		AccountJid: s.account.JID, Jid: peer,
	}); err != nil {
		slog.Warn("persisting local roster entry for new chat", "contact", peer, "err", err)
	}
	s.broadcastChat(ctx, srv, accountIdx, peer, "")
}
