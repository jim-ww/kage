package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func expandModel() Model {
	long := Message{ID: "long", Author: "Bob", Content: strings.Repeat("line\n", 40) + "end", SentAt: time.Now()}
	short := Message{ID: "short", Author: "Bob", Content: "hi", SentAt: time.Now()}
	m := newTestModelWithMessages([]Message{long, short}, nil)
	m.selectedView = viewChat
	m.width, m.termHeight = 80, 30
	m.updateSizes()
	m.refreshViewport()
	return m
}

func msgMenuLabels(m Model, idx int) []string {
	var labels []string
	for _, item := range m.messageContextMenuItems(idx) {
		labels = append(labels, item.label)
	}
	return labels
}

func TestExpandMessageFromKeyboard(t *testing.T) {
	m := expandModel()
	m.selectedMsg = 0
	collapsedLines := len(m.viewportLines)
	if labels := strings.Join(msgMenuLabels(m, 0), ","); !strings.Contains(labels, "Expand") {
		t.Fatalf("long message's menu = %s, want an Expand item", labels)
	}

	ctrlX := tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	m = run(t, m, ctrlX)
	if len(m.viewportLines) <= collapsedLines {
		t.Fatalf("ctrl+x didn't expand: %d lines, was %d", len(m.viewportLines), collapsedLines)
	}
	if labels := strings.Join(msgMenuLabels(m, 0), ","); !strings.Contains(labels, "Collapse") {
		t.Fatalf("expanded message's menu = %s, want a Collapse item", labels)
	}

	m = run(t, m, ctrlX)
	if len(m.viewportLines) != collapsedLines {
		t.Fatalf("second ctrl+x didn't collapse: %d lines, want %d", len(m.viewportLines), collapsedLines)
	}

	m.selectedMsg = 1
	m = run(t, m, ctrlX)
	if m.expandedMsgs["short"] || m.noticeText == "" {
		t.Fatal("ctrl+x on a short message should change nothing and say why")
	}
	if labels := strings.Join(msgMenuLabels(m, 1), ","); strings.Contains(labels, "Expand") {
		t.Fatalf("short message's menu = %s, want no Expand item", labels)
	}
}
