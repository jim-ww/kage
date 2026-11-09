//go:build integration

package xmpp

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// TestAvatarRemovalSeenByContact checks that after alice takes her avatar
// down, a contact's metadata fetch reports "no avatar" (ok false, no error)
// rather than an error - the daemon only drops its cached copy on the
// former.
func TestAvatarRemovalSeenByContact(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()
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

	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	id, err := alice.PublishAvatar(ctx, buf.Bytes(), "image/png", 4, 4)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	info, ok, err := bob.FetchAvatarMetadata(ctx, "alice@localhost")
	if err != nil || !ok || info.ID != id {
		t.Fatalf("after publishing: info=%+v ok=%v err=%v, want id %s", info, ok, err, id)
	}

	if err := alice.DeleteAvatar(ctx); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if info, ok, err = bob.FetchAvatarMetadata(ctx, "alice@localhost"); err != nil || ok {
		t.Fatalf("after deleting: info=%+v ok=%v err=%v, want no avatar and no error", info, ok, err)
	}
}
