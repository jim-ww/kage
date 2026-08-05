package xmpp

import (
	_ "crypto/sha1" // registers SHA-1 so crypto.SHA1.HashFunc().New() works, per XEP-0115's mandated hash

	"mellium.im/xmpp/crypto"
	"mellium.im/xmpp/disco"
	"mellium.im/xmpp/disco/info"
	"mellium.im/xmpp/mux"
)

// discoCapsNode identifies kage for XEP-0115 entity capabilities. It doesn't
// need to resolve to anything; it just needs to be a stable string unique to
// this client so the "node" half of the caps hash (node#ver) is meaningful.
const discoCapsNode = "https://github.com/jim-ww/kage"

// discoFeatures/discoIdentity are what kage advertises via XEP-0030 (Service
// Discovery) about itself. Without this, other clients have no way to learn
// we support OMEMO at all: entity capabilities (XEP-0115) and any direct
// disco#info query both depend on the responder actually answering, and
// before this file existed nothing did - incoming <iq/> queries got no
// mux at all, so disco#info requests for our JID silently went unanswered.
// The "...+notify" feature strings are also what tells a PEP publisher's own
// contacts to auto-subscribe to that node (device-list updates in
// particular), per XEP-0163.
var discoFeatures = featureList{
	"http://jabber.org/protocol/disco#info",
	"http://jabber.org/protocol/disco#items",
	"urn:xmpp:omemo:2:devices+notify",
	"eu.siacs.conversations.axolotl.devicelist+notify",
	"urn:xmpp:openpgp:0:public-keys+notify",
	"http://jabber.org/protocol/chatstates",
}

var discoIdentity = identityList{
	{Category: "client", Type: "pc", Name: "kage"},
}

type featureList []string

func (fl featureList) ForFeatures(node string, f func(info.Feature) error) error {
	if node != "" {
		return nil
	}
	for _, v := range fl {
		if err := f(info.Feature{Var: v}); err != nil {
			return err
		}
	}
	return nil
}

type identityList []info.Identity

func (il identityList) ForIdentities(node string, f func(info.Identity) error) error {
	if node != "" {
		return nil
	}
	for _, id := range il {
		if err := f(id); err != nil {
			return err
		}
	}
	return nil
}

// newDiscoMux builds the ServeMux that answers incoming disco#info/disco#items
// queries (and, via mux's default fallback, replies with a proper
// service-unavailable error to any other IQ type we don't otherwise handle,
// instead of the total silence every incoming <iq/> got before this existed).
//
// HandleWithURI (rather than plain Handle) additionally lets a disco#info
// query addressed to our caps node (discoCapsNode+"#"+ver, as advertised in
// our presence's <c/> - see discoCaps) resolve to the same base feature/
// identity list, per XEP-0115.
func newDiscoMux() *mux.ServeMux {
	return mux.New("jabber:client",
		disco.HandleWithURI(discoCapsNode, crypto.SHA1.HashFunc().New()),
		mux.Feature(discoFeatures),
		mux.Ident(discoIdentity),
	)
}

// discoCaps computes this client's XEP-0115 entity capabilities element, to
// be included in our outgoing presence. Without this, contacts have no
// signal that we support anything beyond plain messaging - disco#info to a
// bare JID is answered by the server itself (see Info's doc comment), not
// forwarded to us, so entity caps carried in presence are the only way a
// contact's client learns what a specific resource (us) supports and knows
// to go query it.
func discoCaps() disco.Caps {
	info := disco.Info{
		Features: []info.Feature(discoFeatures.asFeatures()),
		Identity: []info.Identity(discoIdentity),
	}
	return disco.Caps{
		Hash: crypto.SHA1,
		Node: discoCapsNode,
		Ver:  info.Hash(crypto.SHA1.HashFunc().New()),
	}
}

func (fl featureList) asFeatures() []info.Feature {
	out := make([]info.Feature, len(fl))
	for i, v := range fl {
		out[i] = info.Feature{Var: v}
	}
	return out
}
