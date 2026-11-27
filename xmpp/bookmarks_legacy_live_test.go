//go:build integration

package xmpp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"slices"
	"strings"
	"testing"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/stanza"
)

// TestLegacyBookmarksLive reads legacy bookmarks an older client wrote,
// and removes one without touching the rest.
func TestLegacyBookmarksLive(t *testing.T) {
	ctx := context.Background()
	tlsConfig := devtestTLSConfig(t)
	var b [8]byte
	_, _ = rand.Read(b[:])
	addr := "kagetest-" + hex.EncodeToString(b[:]) + "@localhost"
	if err := Register(ctx, addr, "pw", tlsConfig); err != nil {
		t.Skipf("registering %s: %v", addr, err)
	}
	c, err := Dial(ctx, addr, "pw", tlsConfig)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if got, err := c.LegacyBookmarks(ctx); err != nil || len(got) != 0 {
		t.Fatalf("before any: %+v, %v", got, err)
	}
	stored := `<storage xmlns='storage:bookmarks'><conference jid='a@conference.localhost' autojoin='true'><nick>n</nick></conference><conference jid='b@conference.localhost' autojoin='true'/><url name='x' url='https://example.org'/></storage>`
	query := xmlstream.Wrap(xml.NewDecoder(strings.NewReader(stored)), xml.StartElement{Name: xml.Name{Space: privateNS, Local: "query"}})
	if err := c.session.UnmarshalIQElement(ctx, query, stanza.IQ{Type: stanza.SetIQ}, nil); err != nil {
		t.Fatalf("storing legacy bookmarks: %v", err)
	}
	got, err := c.LegacyBookmarks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Bookmark{{Room: "a@conference.localhost", Nick: "n", Autojoin: true}, {Room: "b@conference.localhost", Autojoin: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("legacy bookmarks = %+v", got)
	}
	if err := c.RemoveLegacyBookmark(ctx, "a@conference.localhost"); err != nil {
		t.Fatalf("removing: %v", err)
	}
	if got, err = c.LegacyBookmarks(ctx); err != nil || !slices.Equal(got, want[1:]) {
		t.Errorf("after removal: %+v, %v", got, err)
	}
	storage, err := c.legacyStorage(ctx)
	if err != nil || !slices.ContainsFunc(storage.Elements, func(e legacyElem) bool { return e.XMLName.Local == "url" }) {
		t.Errorf("url bookmark lost: %+v, %v", storage, err)
	}
}
