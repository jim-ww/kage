package ui

import (
	"net/url"
	"strings"
)

// xmppURI is an xmpp: link (RFC 5122, XEP-0147) cut into its address,
// action ("join", "message", ...) and parameters.
type xmppURI struct {
	addr   string
	action string
	params map[string]string
}

// parseAddress reads s as an xmpp: link, or as a plain address when it
// isn't one, so either can be pasted where an address is asked for.
func parseAddress(s string) xmppURI {
	s = strings.TrimSpace(s)
	rest, ok := strings.CutPrefix(s, "xmpp:")
	if !ok {
		if lower := strings.ToLower(s); strings.HasPrefix(lower, "xmpp:") {
			rest, ok = s[len("xmpp:"):], true
		}
	}
	if !ok {
		return xmppURI{addr: s}
	}
	// An authority ("xmpp://me@host/...") names the account to act as,
	// which isn't the address.
	if after, found := strings.CutPrefix(rest, "//"); found {
		if slash := strings.IndexByte(after, '/'); slash >= 0 {
			rest = after[slash+1:]
		} else {
			rest = ""
		}
	}
	path, query, _ := strings.Cut(rest, "?")
	if frag := strings.IndexByte(query, '#'); frag >= 0 {
		query = query[:frag]
	}
	u := xmppURI{addr: path, params: map[string]string{}}
	if a, err := url.PathUnescape(path); err == nil {
		u.addr = a
	}
	parts := strings.Split(query, ";")
	u.action = parts[0]
	for _, p := range parts[1:] {
		k, v, _ := strings.Cut(p, "=")
		if uv, err := url.QueryUnescape(v); err == nil {
			v = uv
		}
		u.params[k] = v
	}
	return u
}
