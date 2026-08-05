package main

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// handleIncomingMessage decrypts, persists, and forwards a single incoming
// chat message — routing XEP-0308 corrections to storage.UpdateMessageBodyByID
// and a MessageCorrectedMsg instead of appending a new message, and XEP-0424
// retractions to a flag (never an actual delete — see ui.Message.Retracted).
func handleIncomingMessage(ctx context.Context, p *tea.Program, accountIdx int, s *accountSession, msgEv xmpp.MessageEvent) {
	if msgEv.ReactionTargetID != "" {
		from := bareJID(msgEv.From)
		if err := replaceReactions(ctx, s, from, msgEv.ReactionTargetID, from, msgEv.Reactions); err != nil {
			debugf("warning: persisting reactions from %s: %v\n", from, err)
		}
		p.Send(ui.MessageReactionsMsg{
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
			debugf("warning: persisting retraction flag: %v\n", err)
		}
		p.Send(ui.MessageRetractedMsg{
			AccountIdx: accountIdx,
			From:       from,
			RetractID:  msgEv.RetractID,
		})
		return
	}

	body := msgEv.Body
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
				debugf("omemo message %s from %s already stored, skipping re-decrypt", msgEv.ID, msgEv.From)
				return
			}
		}

		debugf("received omemo message from %s for %s", msgEv.From, s.account.JID)

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
			debugf("warning: decoding omemo message from %s: %v\n", msgEv.From, err)
			return
		}
		if mgr == nil {
			debugf("warning: received omemo message from %s but omemo isn't ready for %s\n", msgEv.From, s.account.JID)
			return
		}
		pt, err := mgr.DecryptMessage(ctx, enc)
		if errors.Is(err, omemolib.ErrOwnDeviceKeyMissing) {
			// Not a real failure - this stanza just wasn't encrypted for this
			// device's current key (e.g. it also targets other/older devices
			// on the same account). Nothing was lost, so stay quiet instead
			// of cluttering the chat with a per-device non-error.
			debugf("omemo message from %s has no key for this device, skipping", msgEv.From)
			return
		} else if err != nil {
			// Surface the failure in the chat instead of dropping the message
			// entirely - a silently vanished message looks indistinguishable
			// from "nothing was sent", which makes this undiagnosable from the UI.
			debugf("decrypting omemo message from %s failed: %v", msgEv.From, err)
			body = "[message could not be decrypted: " + err.Error() + "]"
		} else if pt == nil {
			debugf("omemo message from %s was key-transport only (no content)", msgEv.From)
			return // key-transport message: session established/refreshed, no content to show
		} else {
			debugf("omemo message from %s decrypted successfully", msgEv.From)
			body = string(pt)
		}
	}
	if s.useGPG && gpg.Looks(body) {
		pt, err := s.gpg.Decrypt(body, s.account.GPGPeers[msgEv.From])
		if err != nil {
			debugf("warning: decrypting message from %s: %v\n", msgEv.From, err)
		} else {
			body = pt
		}
	}
	// Encrypted replies carry their reply-quote in-band (adapter.go's send()
	// prepends "> ..." lines to the plaintext before encrypting, since a
	// wire-level XEP-0461 <fallback> range can't mark up ciphertext) - strip
	// it back off now that we've decrypted, mirroring what the unencrypted
	// path already gets for free from the <fallback> element (see
	// xmpp/events.go's Body doc comment). Left in place, this "> quoted\n"
	// prefix breaks attachmentURLs' scheme prefix match below for a reply to
	// (or consisting of) a file. Applied unconditionally rather than gated
	// on msgEv.ReplyToID: some peers (e.g. this doesn't always round-trip
	// through our own <reply/> parsing) still send an in-band quote without
	// a recognized reply element, and stripReplyQuote is a no-op on a body
	// that doesn't start with one anyway.
	body = stripReplyQuote(body)

	from := bareJID(msgEv.From) // chats are keyed by bare JID (roster entries); From with a resource never matches

	if msgEv.ReplaceID != "" {
		sealedBody, encrypted := encryptForStorage(s, body)
		if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
			AccountJid:   s.account.JID,
			Body:         sealedBody,
			Encrypted:    encrypted,
			E2eEncrypted: e2eEncrypted,
			E2eeMethod:   nullString(e2eeMethod),
			IDAttr:       nullString(msgEv.ReplaceID),
			RosterJid:    nullString(from),
		}); err != nil {
			debugf("warning: persisting correction: %v\n", err)
		}
		p.Send(ui.MessageCorrectedMsg{
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
			debugf("message %s from %s already stored, skipping duplicate", msgEv.ID, msgEv.From)
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
	}); err != nil {
		// Insertion failed (most likely the unique index rejecting a
		// duplicate idAttr that slipped past the check above in a race) -
		// don't forward it to the UI, or a message with no corresponding
		// stored row appears in chat and then vanishes on next reload.
		debugf("warning: persisting received message: %v\n", err)
		return
	}

	p.Send(ui.IncomingMessageMsg{
		AccountIdx: accountIdx,
		From:       from,
		ReplyToID:  msgEv.ReplyToID,
		Message: ui.Message{
			ID:          msgEv.ID,
			Author:      s.rosterName(from),
			Content:     body,
			SentAt:      msgEv.SentAt,
			IsMe:        false,
			Encrypted:   e2eEncrypted,
			EncMethod:   e2eeMethod,
			Attachments: attachmentURLs(body),
		},
	})
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

// attachmentURLs recognizes the URL-only message produced by SendFile. A
// plain link body remains visible as fallback text for every XMPP client,
// while Kage additionally exposes it as a downloadable attachment.
// Also recognizes aesgcm:// URLs (XEP-0454 encrypted file sharing).
func attachmentURLs(body string) []string {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "https://") || strings.HasPrefix(body, "http://") {
		return []string{body}
	}
	if strings.HasPrefix(body, "aesgcm://") {
		return []string{body}
	}
	return nil
}
