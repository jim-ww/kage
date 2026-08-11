package xmpp

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"

	"mellium.im/xmlstream"
	"mellium.im/xmpp"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/stanza"
	"mellium.im/xmpp/stream"
)

// registerFeatureNS is XEP-0077's stream feature, advertised by a server
// pre-authentication to signal that in-band registration (registerNS) is
// allowed on this stream.
const (
	registerFeatureNS = "http://jabber.org/features/iq-register"
	registerNS        = "jabber:iq:register"
)

// Register creates an account on address's server via XEP-0077 in-band
// registration, then closes the connection — it does not log in. Callers
// should Dial with the same credentials afterward. tlsConfig is optional,
// same as Dial's.
//
// address is a bare or full JID; only its domain and localpart are used (the
// localpart becomes the registered username).
func Register(ctx context.Context, address, password string, tlsConfig *tls.Config) error {
	j, err := jid.Parse(address)
	if err != nil {
		return fmt.Errorf("parsing jid %q: %w", address, err)
	}
	username := j.Localpart()
	if username == "" {
		return fmt.Errorf("jid %q has no localpart to register", address)
	}

	if tlsConfig == nil {
		tlsConfig = &tls.Config{ServerName: j.Domain().String()}
	}

	var regErr error
	session, err := xmpp.DialClientSession(
		ctx, j,
		xmpp.StartTLS(tlsConfig),
		registerFeature(username, password, &regErr),
	)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", j, err)
	}
	closeErr := session.Close()
	if regErr != nil {
		return regErr
	}
	if closeErr != nil {
		return fmt.Errorf("closing registration session: %w", closeErr)
	}
	return nil
}

// registerIQ mirrors mellium's own (unexported) legacy-IQ stream features
// (see bind.go): resource binding and in-band registration are both
// negotiated as a plain get/set IQ round trip before normal stanza handling
// begins, rather than through SASL-style challenge/response.
type registerIQ struct {
	stanza.IQ

	Query registerQuery `xml:"jabber:iq:register query"`
	Err   *stanza.Error `xml:"error,omitempty"`
}

type registerQuery struct {
	Username string `xml:"username"`
	Password string `xml:"password"`
}

func (rq registerQuery) TokenReader() xml.TokenReader {
	return xmlstream.Wrap(
		xmlstream.MultiReader(
			xmlstream.Wrap(xmlstream.Token(xml.CharData(rq.Username)), xml.StartElement{Name: xml.Name{Local: "username"}}),
			xmlstream.Wrap(xmlstream.Token(xml.CharData(rq.Password)), xml.StartElement{Name: xml.Name{Local: "password"}}),
		),
		xml.StartElement{Name: xml.Name{Space: registerNS, Local: "query"}},
	)
}

func (riq *registerIQ) WriteXML(w xmlstream.TokenWriter) (int, error) {
	if riq.Err != nil {
		return xmlstream.Copy(w, riq.Wrap(xmlstream.Wrap(riq.Err.TokenReader(), xml.StartElement{Name: xml.Name{Space: registerNS, Local: "query"}})))
	}
	return xmlstream.Copy(w, riq.Wrap(riq.Query.TokenReader()))
}

// registerFeature performs XEP-0077 registration as a stream feature
// negotiation: it's advertised only pre-authentication (Prohibited: Authn),
// so selecting it lets us send the registration IQs before SASL is even in
// play — SASL is deliberately absent from Register's declared feature list,
// so it's never selected and the stream is simply left unauthenticated once
// registration finishes (or fails); *outErr receives the registration
// result, since a StreamFeature can't itself abort DialClientSession short
// of returning a fatal error (which Register turns into a hard error anyway
// via regErr).
func registerFeature(username, password string, outErr *error) xmpp.StreamFeature {
	return xmpp.StreamFeature{
		Name:       xml.Name{Space: registerFeatureNS, Local: "register"},
		Prohibited: xmpp.Authn,
		Parse: func(ctx context.Context, d *xml.Decoder, start *xml.StartElement) (bool, interface{}, error) {
			parsed := struct {
				XMLName xml.Name `xml:"http://jabber.org/features/iq-register register"`
			}{}
			return false, nil, d.DecodeElement(&parsed, start)
		},
		Negotiate: func(ctx context.Context, session *xmpp.Session, data interface{}) (xmpp.SessionState, io.ReadWriter, error) {
			var mask xmpp.SessionState
			r := session.TokenReader()
			defer r.Close()
			d := xml.NewTokenDecoder(r)
			w := session.TokenWriter()
			defer w.Close()

			reqID := randomID()
			req := &registerIQ{
				IQ: stanza.IQ{
					XMLName: xml.Name{Space: stanza.NSClient, Local: "iq"},
					ID:      reqID,
					Type:    stanza.SetIQ,
				},
				Query: registerQuery{Username: username, Password: password},
			}
			if _, err := req.WriteXML(w); err != nil {
				*outErr = err
				return mask, nil, nil
			}
			if err := w.Flush(); err != nil {
				*outErr = err
				return mask, nil, nil
			}

			tok, err := d.Token()
			if err != nil {
				*outErr = err
				return mask, nil, nil
			}
			start, ok := tok.(xml.StartElement)
			if !ok {
				*outErr = fmt.Errorf("registering: %w", stream.BadFormat)
				return mask, nil, nil
			}
			resp := registerIQ{}
			switch start.Name {
			case xml.Name{Space: stanza.NSClient, Local: "iq"}:
				if err := d.DecodeElement(&resp, &start); err != nil {
					*outErr = err
					return mask, nil, nil
				}
			default:
				*outErr = fmt.Errorf("registering: %w", stream.BadFormat)
				return mask, nil, nil
			}

			switch {
			case resp.ID != reqID:
				*outErr = fmt.Errorf("registering: %w", stream.UndefinedCondition)
			case resp.Type == stanza.ResultIQ:
				// registered
			case resp.Type == stanza.ErrorIQ && resp.Err != nil:
				*outErr = fmt.Errorf("registering: %w", *resp.Err)
			default:
				*outErr = fmt.Errorf("registering: unexpected response type %q", resp.Type)
			}
			// Never negotiate further (no Authn/Ready mask, no stream restart) -
			// Register closes the session right after this regardless of outcome.
			return mask, nil, nil
		},
	}
}
