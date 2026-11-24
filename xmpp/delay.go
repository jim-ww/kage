package xmpp

import "time"

// delayElem is a XEP-0203 <delay/>: when a stanza was originally sent, added
// by whoever held on to it - our server for an offline message, either end
// for one re-sent after a lost stream (XEP-0198 §4).
type delayElem struct {
	Stamp string `xml:"stamp,attr"`
}

// parseStamp parses a XEP-0082 DateTime, as a <delay/> carries it, into
// local time; ok is false if it isn't one.
func parseStamp(stamp string) (t time.Time, ok bool) {
	// RFC3339 parsing also takes the optional fractional seconds.
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return time.Time{}, false
	}
	return t.Local(), true
}

// notAfter is t, or now if t is later: a stamp from a clock running ahead,
// or a forged one, mustn't sort a message after everything yet to come.
func notAfter(t, now time.Time) time.Time {
	if t.After(now) {
		return now
	}
	return t
}

// sentAt is when a message received at now was sent: the earliest of its
// delays' stamps, as more than one may have been added along the way and
// each is when it was sent or later, or now if none parses.
func sentAt(now time.Time, delays []delayElem) time.Time {
	at := now
	for _, d := range delays {
		if t, ok := parseStamp(d.Stamp); ok && t.Before(at) {
			at = t
		}
	}
	return at
}
