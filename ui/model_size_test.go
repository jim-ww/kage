package ui

import (
	"testing"
	"unsafe"
)

// TestModelSizeStaysBounded guards against re-inflating Model: it used to
// embed several bubbles components by value (list.Model, textarea.Model,
// filepicker.Model, three textinput.Model fields, and a [4]textinput.Model
// array) at over 130KB total. Model is passed by value throughout this
// package — View, chatAreaWidth, renderMessage, and dozens of other
// read-only helpers all take `m Model`, and several of those (renderMessage
// in particular) run once per rendered message — so every one of those
// calls was copying the *entire* 130KB+ struct. That's what made the
// message cursor and mouse-hover highlight visibly lag during rapid
// movement: not the rendering logic itself, but the struct copy underneath
// every helper call. Those fields are pointers now (see model.go's comment
// by the chats field) — this pins the win at ~30KB so it can't silently
// regress by someone adding a large field back by value.
func TestModelSizeStaysBounded(t *testing.T) {
	const maxSize = 40_000 // bytes; comfortably above today's ~30KB, well below the ~136KB this was fixed from
	var m Model
	if got := unsafe.Sizeof(m); got > maxSize {
		t.Fatalf("sizeof(Model) = %d bytes, want <= %d — Model is passed by value throughout ui/*.go, so growing it re-inflates the cost of every read-only helper call (see this test's doc comment)", got, maxSize)
	}
}
