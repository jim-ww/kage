package xmpp

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"strings"
	"testing"
)

// The avatar PEP push is what tells us a contact changed their picture;
// without this routing it only ever updates when they next come online in a
// fresh daemon run. See account.go's AvatarChangedEvent handling.
func TestPubsubAvatarEventDecode(t *testing.T) {
	const push = `<message xmlns="jabber:client" from="bob@example.com" to="alice@example.com">
  <event xmlns="http://jabber.org/protocol/pubsub#event">
    <items node="urn:xmpp:avatar:metadata">
      <item id="111f4b3c50d7b0df729d299bc6f8e9ef9066971f">
        <metadata xmlns="urn:xmpp:avatar:metadata">
          <info bytes="12345" id="111f4b3c50d7b0df729d299bc6f8e9ef9066971f" type="image/png"/>
        </metadata>
      </item>
    </items>
  </event>
</message>`

	d := xml.NewDecoder(strings.NewReader(push))
	tok, err := d.Token()
	if err != nil {
		t.Fatal(err)
	}
	start := tok.(xml.StartElement)
	var msg messageBody
	if err := d.DecodeElement(&msg, &start); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.PubsubEvent == nil || msg.PubsubEvent.Items == nil {
		t.Fatal("PubsubEvent.Items is nil")
	}
	if got := msg.PubsubEvent.Items.Node; got != avatarMetadataNode {
		t.Errorf("Items.Node = %q, want %q", got, avatarMetadataNode)
	}
}

func TestAvatarMetadataDecode(t *testing.T) {
	tests := []struct {
		name    string
		xml     string
		wantID  string
		wantTyp string
		wantOK  bool
	}{
		{
			name:    "single png",
			xml:     `<metadata xmlns="urn:xmpp:avatar:metadata"><info bytes="12345" id="abc123" type="image/png"/></metadata>`,
			wantID:  "abc123",
			wantTyp: "image/png",
			wantOK:  true,
		},
		{
			// XEP-0084 uses an empty <metadata/> to mean the avatar was
			// removed, which must not read as an error.
			name:   "avatar removed",
			xml:    `<metadata xmlns="urn:xmpp:avatar:metadata"/>`,
			wantOK: false,
		},
		{
			// A rendition with a url lives over HTTP, not in the data node;
			// the in-band one after it is the one we can actually fetch.
			name: "http rendition skipped",
			xml: `<metadata xmlns="urn:xmpp:avatar:metadata">
				<info bytes="9" id="remote" type="image/png" url="https://example.com/a.png"/>
				<info bytes="9" id="local" type="image/jpeg"/>
			</metadata>`,
			wantID:  "local",
			wantTyp: "image/jpeg",
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var meta metadataElem
			if err := xml.Unmarshal([]byte(tt.xml), &meta); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			var gotID, gotTyp string
			var ok bool
			for _, in := range meta.Info {
				if in.URL != "" || in.ID == "" {
					continue
				}
				gotID, gotTyp, ok = in.ID, in.Type, true
				break
			}
			if ok != tt.wantOK {
				t.Fatalf("found = %v, want %v", ok, tt.wantOK)
			}
			if gotID != tt.wantID || gotTyp != tt.wantTyp {
				t.Errorf("got (%q, %q), want (%q, %q)", gotID, gotTyp, tt.wantID, tt.wantTyp)
			}
		})
	}
}

// Publishers wrap the base64 payload at whatever width they like, and the
// decoder rejects embedded newlines.
func TestAvatarDataDecodeWrapped(t *testing.T) {
	raw := []byte("some avatar bytes, pretend they're a png")
	encoded := base64.StdEncoding.EncodeToString(raw)
	wrapped := "\n      " + encoded[:8] + "\n      " + encoded[8:] + "\n    "

	var payload avatarDataElem
	doc := `<data xmlns="urn:xmpp:avatar:data">` + wrapped + `</data>`
	if err := xml.Unmarshal([]byte(doc), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(payload.Data), ""))
	if err != nil {
		t.Fatalf("decoding unwrapped payload: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("got %q, wanted the original bytes back", got)
	}
}

// The metadata node advertises a contact-controlled byte count and the data
// arrives as one base64 stanza, so both need a bound.
func TestAvatarMaxBytesIsSane(t *testing.T) {
	if AvatarMaxBytes <= 0 {
		t.Fatal("AvatarMaxBytes must be positive")
	}
	if AvatarMaxBytes > 8<<20 {
		t.Errorf("AvatarMaxBytes = %d, too large to accept in a single stanza", AvatarMaxBytes)
	}
}

// Without the +notify feature a contact's server never pushes avatar
// changes to us at all (XEP-0163), so the PEP routing above never fires.
func TestAvatarMetadataNotifyAdvertised(t *testing.T) {
	want := avatarMetadataNode + "+notify"
	for _, f := range discoFeatures {
		if f == want {
			return
		}
	}
	t.Errorf("discoFeatures is missing %q", want)
}

// A contact decides whether to re-download by comparing the advertised id
// against what they cached, and the daemon's cache does the same by hashing
// the file — so the published id must be the SHA-1 of the published bytes
// and nothing else.
func TestAvatarIDIsContentHash(t *testing.T) {
	data := []byte("pretend this is a png")
	sum := sha1.Sum(data)
	want := hex.EncodeToString(sum[:])

	got := avatarID(data)
	if got != want {
		t.Fatalf("avatarID = %q, want the SHA-1 %q", got, want)
	}
	if len(got) != 40 {
		t.Errorf("avatarID is %d chars, want 40 hex chars", len(got))
	}
	if same := avatarID(data); same != got {
		t.Error("avatarID is not stable for identical bytes")
	}
	if other := avatarID([]byte("different bytes")); other == got {
		t.Error("avatarID collided for different bytes")
	}
}

// An empty <metadata/> is how XEP-0084 says "no avatar"; if it decoded as
// anything else, removal would read as a broken publish on the far side.
func TestEmptyMetadataMeansNoAvatar(t *testing.T) {
	var meta metadataElem
	if err := xml.Unmarshal([]byte(`<metadata xmlns="urn:xmpp:avatar:metadata"/>`), &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(meta.Info) != 0 {
		t.Errorf("empty metadata decoded %d info entries, want 0", len(meta.Info))
	}
}

func TestPublishAvatarRejectsUnusableInput(t *testing.T) {
	var c Client
	if _, err := c.PublishAvatar(t.Context(), nil, "image/png", 0, 0); err == nil {
		t.Error("PublishAvatar accepted empty data")
	}
	if _, err := c.PublishAvatar(t.Context(), make([]byte, AvatarMaxBytes+1), "image/png", 0, 0); err == nil {
		t.Error("PublishAvatar accepted data over the size limit")
	}
}
