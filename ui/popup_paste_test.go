package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

func pasteTestModel(t *testing.T) Model {
	t.Helper()
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	chat := Chat{Address: "bob@example.test"}
	m.accounts = []Account{{Chats: []list.Item{chat}, Messages: map[int][]Message{0: {{ID: "a", Content: "hi"}}}}}
	m.chats.SetItems(m.accounts[0].Chats)
	m.chats.Select(0)
	m.selectedView = viewChat
	m.selectedMsg = 0
	m.refreshViewport()
	return m
}

func paste(m Model, text string) Model {
	next, _ := m.Update(tea.PasteMsg{Content: text})
	return next.(Model)
}

// TestPasteIntoEmojiPickerSearch checks a paste while reacting goes to the
// picker's search box, not the compose box behind it.
func TestPasteIntoEmojiPickerSearch(t *testing.T) {
	m := pasteTestModel(t)
	m.actionReactMessage()
	m = paste(m, "fire")
	if got := m.input.Value(); got != "" {
		t.Fatalf("paste leaked into the compose box: %q", got)
	}
	if !strings.Contains(m.emojiPicker.View(), "fire") {
		t.Fatal("paste didn't reach the picker's search box")
	}
}

// TestPasteDoesNotLeakBehindPopups checks a paste while a popup with no text
// field is open lands nowhere rather than in the compose box behind it.
func TestPasteDoesNotLeakBehindPopups(t *testing.T) {
	for name, open := range map[string]func(*Model){
		"help":         func(m *Model) { m.showHelp = true },
		"message info": func(m *Model) { m.showMsgInfo = true },
		"confirm":      func(m *Model) { m.confirmTarget = confirmQuit },
		"context menu": func(m *Model) {
			m.openContextMenu("x", []contextMenuItem{{label: "a", run: func(*Model) tea.Cmd { return nil }}})
		},
	} {
		m := pasteTestModel(t)
		open(&m)
		m = paste(m, "oops")
		if got := m.input.Value(); got != "" {
			t.Errorf("%s: paste leaked into the compose box: %q", name, got)
		}
	}
}

// TestPasteIntoPromptInputs is the sanity check that every single-line
// prompt still takes a paste.
func TestPasteIntoPromptInputs(t *testing.T) {
	m := pasteTestModel(t)
	m.actionRenameChat()
	if m = paste(m, "Bobby"); m.renameInput.Value() != "Bobby" {
		t.Errorf("rename: %q", m.renameInput.Value())
	}

	m = pasteTestModel(t)
	m.actionSearchChat()
	if m = paste(m, "needle"); m.searchInput.Value() != "needle" {
		t.Errorf("search: %q", m.searchInput.Value())
	}

	m = pasteTestModel(t)
	m.openSaveAsPrompt("https://example.test/f.pdf")
	m.saveAsInput.SetValue("")
	if m = paste(m, "/tmp/f.pdf"); m.saveAsInput.Value() != "/tmp/f.pdf" {
		t.Errorf("save as: %q", m.saveAsInput.Value())
	}

	m = pasteTestModel(t)
	if m = paste(m, "hello"); m.input.Value() != "hello" {
		t.Errorf("compose: %q", m.input.Value())
	}
}
