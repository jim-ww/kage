package main

import "testing"

func TestShouldNotifySkipsMutedChats(t *testing.T) {
	prev := notifyEnabled.Load()
	notifyEnabled.Store(true)
	t.Cleanup(func() {
		notifyEnabled.Store(prev)
		mutedChats.replace(nil)
	})

	if !shouldNotify("me@example.org", "bob@example.org", false, false) {
		t.Fatal("unmuted chat doesn't notify")
	}
	mutedChats.flag("me@example.org", "Bob@Example.org", true)
	if shouldNotify("me@example.org", "bob@example.org", false, false) {
		t.Fatal("muted chat still notifies")
	}
	if !shouldNotify("other@example.org", "bob@example.org", false, false) {
		t.Fatal("muting on one account silenced another")
	}
	mutedChats.flag("me@example.org", "bob@example.org", false)
	if !shouldNotify("me@example.org", "bob@example.org", false, false) {
		t.Fatal("unmuting didn't bring notifications back")
	}
}
