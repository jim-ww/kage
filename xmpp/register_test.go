package xmpp

import (
	"context"
	"encoding/xml"
	"strings"
	"testing"

	"mellium.im/xmpp"
)

// TestRegisterNeedsTLSFirst checks registration waits for TLS, and that
// STARTTLS counts as required, so negotiation goes on to the list after
// it where registration is offered.
func TestRegisterNeedsTLSFirst(t *testing.T) {
	d := xml.NewDecoder(strings.NewReader(`<starttls xmlns='urn:ietf:params:xml:ns:xmpp-tls'/>`))
	tok, err := d.Token()
	if err != nil {
		t.Fatal(err)
	}
	start := tok.(xml.StartElement)
	req, _, err := requiredStartTLS(nil).Parse(context.Background(), d, &start)
	if err != nil || !req {
		t.Errorf("optional STARTTLS parsed as required = %v, err %v", req, err)
	}
	var regErr error
	var offered bool
	if f := registerFeature("u", "p", &regErr, &offered); f.Necessary&xmpp.Secure == 0 {
		t.Error("registration doesn't need a secure stream")
	}
}
