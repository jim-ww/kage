//go:build integration

package xmpp

import (
	"context"
	"testing"
	"time"

	"mellium.im/xmpp/disco"
)

// TestDiscoInfoAdvertisesOmemo verifies that a real disco#info query against
// a connected account gets answered at all, and that the response actually
// lists both OMEMO protocols' "+notify" features - without this, contacts
// have no way to learn we support OMEMO (see disco.go).
func TestDiscoInfoAdvertisesOmemo(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	alice, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()
	bob, err := Dial(ctx, "bob@localhost", "bobpw", tlsConfig)
	if err != nil {
		t.Fatalf("dial bob: %v", err)
	}
	defer bob.Close()

	info, err := disco.GetInfo(ctx, "", alice.session.LocalAddr(), bob.session)
	if err != nil {
		t.Fatalf("bob querying alice's disco#info: %v", err)
	}

	got := make(map[string]bool, len(info.Features))
	for _, f := range info.Features {
		got[f.Var] = true
	}

	for _, want := range []string{
		"urn:xmpp:omemo:2:devices+notify",
		"eu.siacs.conversations.axolotl.devicelist+notify",
	} {
		if !got[want] {
			t.Errorf("disco#info response missing feature %q; got %v", want, info.Features)
		}
	}
}

// TestPresenceAdvertisesCaps verifies alice's initial presence broadcast
// actually carries a XEP-0115 <c/> entity-caps element with our computed
// verification string - without this, a contact's client has no signal that
// we support anything (disco#info to a bare JID is answered by the server,
// never forwarded to us; see disco.go's discoCaps doc comment).
func TestPresenceAdvertisesCaps(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	bob, err := Dial(ctx, "bob@localhost", "bobpw", tlsConfig)
	if err != nil {
		t.Fatalf("dial bob: %v", err)
	}
	defer bob.Close()

	caps := make(chan capsElem, 4)
	go func() {
		for {
			select {
			case ev, ok := <-bob.Events():
				if !ok {
					return
				}
				pe, ok := ev.(PresenceEvent)
				if !ok || pe.Caps == nil {
					continue
				}
				caps <- *pe.Caps
			case <-ctx.Done():
				return
			}
		}
	}()

	alice, err := Dial(ctx, "alice@localhost", "alicepw", tlsConfig)
	if err != nil {
		t.Fatalf("dial alice: %v", err)
	}
	defer alice.Close()

	select {
	case got := <-caps:
		if got.Node != discoCapsNode {
			t.Errorf("caps node = %q, want %q", got.Node, discoCapsNode)
		}
		want := discoCaps()
		if got.Ver != want.Ver {
			t.Errorf("caps ver = %q, want %q", got.Ver, want.Ver)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for alice's presence to carry entity caps")
	}
}
