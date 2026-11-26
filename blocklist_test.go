package main

import (
	"slices"
	"testing"

	"github.com/jim-ww/kage/xmpp"
)

func sorted(s []string) []string {
	slices.Sort(s)
	return s
}

func TestBlockedSetReplaceReportsChanges(t *testing.T) {
	var b blockedSet
	if changed := b.set([]string{"a@x", "b@x"}, true); len(changed) != 2 {
		t.Fatalf("first block changed %v", changed)
	}
	if changed := b.set([]string{"a@x"}, true); len(changed) != 0 {
		t.Errorf("blocking again changed %v", changed)
	}
	blocked, unblocked := b.replace([]string{"b@x", "c@x"})
	if !slices.Equal(blocked, []string{"c@x"}) || !slices.Equal(unblocked, []string{"a@x"}) {
		t.Errorf("replace: blocked %v unblocked %v", blocked, unblocked)
	}
	if !b.has("b@x") || b.has("a@x") {
		t.Errorf("after replace: %v", sorted(b.all()))
	}
}

func TestBlocklistPushUnblockAll(t *testing.T) {
	s := &accountSession{}
	s.blocked.set([]string{"a@x", "b@x"}, true)
	s.applyBlocklistPush(nil, xmpp.BlocklistPushEvent{})
	if got := s.blocked.all(); len(got) != 0 {
		t.Errorf("after an empty unblock push: %v still blocked", got)
	}
	s.applyBlocklistPush(nil, xmpp.BlocklistPushEvent{JIDs: []string{"c@x"}, Blocked: true})
	if !s.blocked.has("c@x") {
		t.Error("block push not applied")
	}
}
