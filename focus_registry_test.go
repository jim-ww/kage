package main

import "testing"

// TestFocusRegistryChatVisible covers the notification-suppression rule:
// a chat counts as on screen only while some attached, focused client
// actually has it open.
func TestFocusRegistryChatVisible(t *testing.T) {
	r := newFocusRegistry()
	bob := focusedChatKey("alice@localhost", "bob@localhost")
	carol := focusedChatKey("alice@localhost", "carol@localhost")

	if r.chatVisible(bob) {
		t.Error("chatVisible with no clients attached = true, want false")
	}

	r.set(1, focusState{focused: true, chatKey: bob})
	if !r.chatVisible(bob) {
		t.Error("chatVisible(open+focused chat) = false, want true")
	}
	if r.chatVisible(carol) {
		t.Error("chatVisible(other chat) = true, want false")
	}

	// Terminal lost OS focus: the chat is open but nobody is looking at it.
	r.set(1, focusState{focused: false, chatKey: bob})
	if r.chatVisible(bob) {
		t.Error("chatVisible(unfocused terminal) = true, want false")
	}
}

// A second TUI viewing the chat keeps it visible, and only the client that
// actually quit is forgotten - the regression being that one global
// focused/open pair let whichever client reported last speak for all of them.
func TestFocusRegistryMultipleClients(t *testing.T) {
	r := newFocusRegistry()
	bob := focusedChatKey("alice@localhost", "bob@localhost")

	r.set(1, focusState{focused: true, chatKey: bob})
	r.set(2, focusState{focused: false, chatKey: ""})
	if !r.chatVisible(bob) {
		t.Error("chatVisible with another client unfocused = false, want true")
	}

	r.forget(1)
	if r.chatVisible(bob) {
		t.Error("chatVisible after the viewing client quit = true, want false")
	}

	// And the reverse: the remaining client's own view still counts.
	r.set(3, focusState{focused: true, chatKey: bob})
	r.forget(2)
	if !r.chatVisible(bob) {
		t.Error("chatVisible after an unrelated client quit = false, want true")
	}
}

// A chat key of "" means "no chat open" - it must never match.
func TestFocusRegistryNoChatOpen(t *testing.T) {
	r := newFocusRegistry()
	r.set(1, focusState{focused: true, chatKey: ""})
	if r.chatVisible("") {
		t.Error(`chatVisible("") = true, want false`)
	}
}
