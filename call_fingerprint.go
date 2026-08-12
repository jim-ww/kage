package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"log/slog"
	"strings"

	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
)

// normalizeFingerprint strips XEP-0320's colon separators and case-folds a
// DTLS-SRTP fingerprint, so two renderings of the same certificate hash
// compare equal regardless of which side produced them.
func normalizeFingerprint(fp string) string {
	return strings.ToUpper(strings.ReplaceAll(fp, ":", ""))
}

// firstFingerprint returns the DTLS-SRTP fingerprint (XEP-0320) carried by
// the first content that has one. In practice every content in one Jingle
// session shares the same value - pion negotiates a single DTLS certificate
// for the whole PeerConnection regardless of how many m-lines it bundles -
// so the first is as good as any.
func firstFingerprint(contents []xmpp.JingleContent) string {
	for _, c := range contents {
		if c.Transport != nil && c.Transport.Fingerprint != nil && c.Transport.Fingerprint.Value != "" {
			return c.Transport.Fingerprint.Value
		}
	}
	return ""
}

// sasAlphabet excludes visually ambiguous characters (0/O, 1/I/L) - it only
// has to be read aloud or eyeballed once per call, not typed.
const sasAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// computeSAS derives a short authentication string (ZRTP-style) from both
// sides' DTLS-SRTP fingerprints, for the two call participants to read aloud
// and compare over a trusted channel (in person, a phone call, ...) - if a
// server in the middle swapped either fingerprint (see the MITM this
// mitigates), the two ends would compute different SAS values and the
// mismatch is immediately obvious without needing to compare full
// certificate hashes character by character.
//
// Sorting the two fingerprints before hashing makes the result independent
// of which side is "local" vs "remote", so both participants land on the
// same 4-character string from the same two certificates.
func computeSAS(fpA, fpB string) string {
	a, b := normalizeFingerprint(fpA), normalizeFingerprint(fpB)
	if a > b {
		a, b = b, a
	}
	sum := sha256.Sum256([]byte(a + "|" + b))
	// Top 20 bits of the hash, 4 groups of 5 bits, each mapped through
	// sasAlphabet - the same shape as a ZRTP SAS.
	val := uint32(sum[0])<<16 | uint32(sum[1])<<8 | uint32(sum[2])
	val >>= 4
	n := uint32(len(sasAlphabet))
	var out [4]byte
	for i := 3; i >= 0; i-- {
		out[i] = sasAlphabet[val%n]
		val /= n
	}
	return string(out[0:2]) + "-" + string(out[2:4])
}

// checkAndPinCallFingerprint implements trust-on-first-use for a call peer's
// DTLS-SRTP certificate: the first call with a contact pins their
// fingerprint (see storage.callPeerFingerprints); every later call compares
// against the pin and reports a mismatch instead of silently accepting
// whatever arrived - the local half of the MITM mitigation described in
// call_fingerprint.go's doc (the SAS above is the manual, every-call half).
// A changed fingerprint isn't refused outright (a contact legitimately
// reinstalling their client produces a new certificate too, and this isn't
// meant to be a hard blocklist) - just flagged, and the pin is updated to
// match so it doesn't keep nagging on every subsequent call.
func checkAndPinCallFingerprint(ctx context.Context, db *storage.Queries, accountJID, peerJID, remoteFP string) (changed bool) {
	norm := normalizeFingerprint(remoteFP)
	if norm == "" || db == nil {
		return false
	}
	cached, err := db.GetCallPeerFingerprint(ctx, storage.GetCallPeerFingerprintParams{AccountJid: accountJID, Jid: peerJID})
	switch {
	case err == nil:
		changed = cached != norm
	case err == sql.ErrNoRows:
		// First call with this contact - nothing to compare against yet.
	default:
		slog.Warn("reading cached call peer fingerprint", "peer", peerJID, "err", err)
	}
	if err := db.UpsertCallPeerFingerprint(ctx, storage.UpsertCallPeerFingerprintParams{
		AccountJid: accountJID, Jid: peerJID, Fingerprint: norm,
	}); err != nil {
		slog.Warn("pinning call peer fingerprint", "peer", peerJID, "err", err)
	}
	return changed
}
