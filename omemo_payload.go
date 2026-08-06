package main

import "encoding/xml"

// omemoPayload is the actual plaintext OMEMO encrypts: not just body text,
// but also which URLs (if any) are file attachments. A XEP-0066
// <x xmlns='jabber:x:oob'> element can't be sent in the clear alongside an
// encrypted body without leaking the attachment URL, so this is its
// structured equivalent, carried inside the ciphertext and parsed back out
// after decrypt instead of guessed from the decrypted text. Other OMEMO
// clients that don't know this wrapper will show it as literal XML - an
// accepted tradeoff since Kage's OMEMO interop is already limited (see
// CLAUDE.md: GPG is the primary encryption path).
type omemoPayload struct {
	XMLName xml.Name `xml:"kage-payload"`
	Body    string   `xml:"body"`
	OOBURLs []string `xml:"oob>url"`
}

// encodeOmemoPayload serializes body/oobURLs into the bytes actually handed
// to OMEMO encryption.
func encodeOmemoPayload(body string, oobURLs []string) []byte {
	out, err := xml.Marshal(omemoPayload{Body: body, OOBURLs: oobURLs})
	if err != nil {
		// xml.Marshal only fails on invalid Go values (channels, funcs) - a
		// string and []string can never trigger this.
		return []byte(body)
	}
	return out
}

// decodeOmemoPayload parses an encodeOmemoPayload envelope back into its
// parts. A plaintext that isn't this envelope - e.g. a real (non-Kage) OMEMO
// client that just sent plain text, or a message from before this wrapper
// existed - is treated as body-only with no attachments: not a guess, just
// what the absence of the envelope actually means.
func decodeOmemoPayload(pt []byte) (body string, oobURLs []string) {
	var p omemoPayload
	if err := xml.Unmarshal(pt, &p); err != nil || p.XMLName.Local != "kage-payload" {
		return string(pt), nil
	}
	return p.Body, p.OOBURLs
}
