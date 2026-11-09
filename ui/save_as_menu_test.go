package ui

import "testing"

// TestMessageMenuSaveAsPromptsForPath checks the right-click menu's "Save
// as" asks where to save, like ctrl+shift+s, rather than saving straight to
// the downloads directory (that's "Save").
func TestMessageMenuSaveAsPromptsForPath(t *testing.T) {
	m := newTestModelWithMessages([]Message{{ID: "a", Content: "see https://example.test/f.pdf"}}, nil)
	m.selectedMsg = 0
	var saveAs, save *contextMenuItem
	items := m.messageContextMenuItems(0)
	for i := range items {
		switch items[i].label {
		case "Save as":
			saveAs = &items[i]
		case "Save":
			save = &items[i]
		}
	}
	if saveAs == nil || save == nil {
		t.Fatalf("menu lacks Save/Save as: %v", items)
	}
	saveAs.run(&m)
	if !m.savingAs || m.saveAsInput == nil {
		t.Fatal("Save as didn't open the destination prompt")
	}
}
