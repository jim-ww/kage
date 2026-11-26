package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/gpg"
	"github.com/jim-ww/kage/storage"
)

func TestGPGPendingPromptsOncePerInterval(t *testing.T) {
	var g gpgPending
	now := time.Now()
	if !g.add(gpgPendingMsg{storeID: 1}, now) {
		t.Fatal("first locked message didn't prompt")
	}
	if g.add(gpgPendingMsg{storeID: 2}, now.Add(time.Second)) {
		t.Fatal("prompted again right away")
	}
	if !g.add(gpgPendingMsg{storeID: 3}, now.Add(gpgLockedPromptInterval)) {
		t.Fatal("no prompt after the interval")
	}
	if got := len(g.take()); got != 3 {
		t.Fatalf("took %d messages, want 3", got)
	}
}

// TestRetryGPGDecrypt checks messages that decrypt now are stored and
// leave the list, and the rest stay for the next retry.
func TestRetryGPGDecrypt(t *testing.T) {
	s := &accountSession{account: config.Account{JID: "alice@example.test"}}
	s.gpgPending.add(gpgPendingMsg{storeID: 1, from: "bob@example.test", armored: "ok"}, time.Now())
	s.gpgPending.add(gpgPendingMsg{storeID: 2, from: "bob@example.test", armored: "locked"}, time.Now())
	decrypt := func(ct, _ string) (string, error) {
		if ct == "locked" {
			return "", errors.New("gpg decrypt: public key decryption failed: Operation cancelled")
		}
		return "hello", nil
	}
	var updated []int64
	update := func(_ context.Context, p storage.UpdateMessageBodyByRowIDParams) error {
		updated = append(updated, p.ID)
		return nil
	}
	err := retryGPGDecrypt(context.Background(), nil, 0, s, decrypt, update)
	if err == nil || !gpg.IsLocked(err) {
		t.Fatalf("err = %v, want the locked message's error", err)
	}
	if len(updated) != 1 || updated[0] != 1 {
		t.Fatalf("updated %v, want [1]", updated)
	}
	left := s.gpgPending.take()
	if len(left) != 1 || left[0].storeID != 2 {
		t.Fatalf("left %+v, want message 2", left)
	}
}

func TestGPGIsLocked(t *testing.T) {
	locked := errors.New("gpg decrypt: exit status 2: gpg: public key decryption failed: Inappropriate ioctl for device\ngpg: decryption failed: No secret key")
	notOurs := errors.New("gpg decrypt: exit status 2: gpg: encrypted with rsa4096 key, ID 0123\ngpg: decryption failed: No secret key")
	if !gpg.IsLocked(locked) || gpg.IsLocked(notOurs) || gpg.IsLocked(nil) {
		t.Fatal("IsLocked misclassified")
	}
}
