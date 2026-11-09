//go:build integration

package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"
)

// TestSyncAvatarDropsRemovedAvatar checks the daemon's cached copy of a
// contact's avatar is deleted once they take it down, instead of staying on
// screen forever.
func TestSyncAvatarDropsRemovedAvatar(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prev := avatarsEnabled.Load()
	avatarsEnabled.Store(true)
	t.Cleanup(func() { avatarsEnabled.Store(prev) })

	ctx := context.Background()
	alice := newCallTestSession(t, ctx, "alice@localhost", "alicepw")
	bob := newCallTestSession(t, ctx, "bob@localhost", "bobpw")

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3, 3))); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.client.Load().PublishAvatar(ctx, buf.Bytes(), "image/png", 3, 3); err != nil {
		t.Fatalf("publish: %v", err)
	}
	bob.syncAvatar(ctx, nil, 0, "alice@localhost")
	if _, ok := cachedAvatarPath("alice@localhost"); !ok {
		t.Fatal("avatar wasn't cached")
	}

	if err := alice.client.Load().DeleteAvatar(ctx); err != nil {
		t.Fatalf("delete: %v", err)
	}
	bob.syncAvatar(ctx, nil, 0, "alice@localhost")
	if path, ok := cachedAvatarPath("alice@localhost"); ok {
		t.Fatalf("removed avatar still cached at %s", path)
	}
}
