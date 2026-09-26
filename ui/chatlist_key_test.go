package ui

import (
	"reflect"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
)

// mutateFieldForTest gives a field a different value of its own type,
// reporting whether it knew how. Kinds it doesn't understand are skipped
// rather than guessed at.
func mutateFieldForTest(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "-mutated")
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(v.Uint() + 1)
	case reflect.Slice:
		v.Set(reflect.Append(v, reflect.New(v.Type().Elem()).Elem()))
	case reflect.Struct:
		t, ok := v.Interface().(time.Time)
		if !ok {
			return false
		}
		v.Set(reflect.ValueOf(t.Add(time.Hour)))
	default:
		return false
	}
	return true
}

// TestChatListKeyCoversEverythingRendered is what stops chatListViewKey
// from rotting as Chat grows. chatListBody reuses the previous frame's
// rendered chat list whenever that key is unchanged, and the key lists
// Chat's fields explicitly (rendering each row to key on its text instead
// costs ~40000x more) — so a field added later that Title or Description
// reads, but the key doesn't, would freeze that change out of the sidebar.
//
// Rather than restating the field list here (which would rot in exactly
// the same way), this walks Chat's fields reflectively: for every field
// that changes what a row renders, the key must change too.
func TestChatListKeyCoversEverythingRendered(t *testing.T) {
	base := Chat{
		Name:        "alice",
		Address:     "alice@example.com",
		LastMessage: "hello there",
		Unread:      2,
		Presence:    PresenceOnline,
	}

	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	m.width, m.termHeight = 120, 40
	m.updateSizes()

	keyFor := func(c Chat) string {
		m.chats.SetItems([]list.Item{c})
		return m.chatListViewKey()
	}
	// What a row actually draws from: DefaultDelegate renders Title and
	// Description, and renderHoverChatRow (the hovered variant) uses the
	// same two.
	renderFor := func(c Chat) string { return c.Title() + "\x00" + c.Description() }

	baseKey, baseRender := keyFor(base), renderFor(base)

	typ := reflect.TypeOf(base)
	checked := 0
	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		mutated := base
		if !mutateFieldForTest(reflect.ValueOf(&mutated).Elem().Field(i)) {
			t.Logf("skipping field %s (%s): no mutation defined for this kind", field.Name, field.Type)
			continue
		}
		checked++

		if renderFor(mutated) == baseRender {
			continue // this field isn't part of a row's rendering
		}
		if keyFor(mutated) == baseKey {
			t.Errorf("Chat.%s changes what a chat row renders but not chatListViewKey — "+
				"the sidebar would keep showing the stale row; add it to chatListViewKey", field.Name)
		}
	}
	if checked == 0 {
		t.Fatal("no Chat fields were exercised — the reflection walk is not doing anything")
	}
}

// TestChatListBodyReusesRenderUntilSomethingChanges covers the cache's two
// halves end to end: an unchanged list must not be re-rendered, and a
// changed one must be.
func TestChatListBodyReusesRenderUntilSomethingChanges(t *testing.T) {
	m := newMouseSweepBenchModel(30, 150)
	_ = m.View()

	first := m.chatListBody()
	if second := m.chatListBody(); second != first {
		t.Error("chat list body changed with no input change")
	}

	// A new unread message on some chat has to show up.
	items := m.chats.Items()
	chat := items[3].(Chat)
	chat.Unread += 7
	chat.LastMessage = "a brand new message arrived"
	items[3] = chat
	m.chats.SetItems(items)

	if after := m.chatListBody(); after == first {
		t.Error("chat list body did not change after a chat's unread count and last message changed")
	}

	// Moving the pointer onto a row restyles it, so that must invalidate too.
	hovered := m.chatListBody()
	m.hover.id = zoneChatItem(5)
	if after := m.chatListBody(); after == hovered {
		t.Error("chat list body did not change after the pointer moved onto a row")
	}
}
