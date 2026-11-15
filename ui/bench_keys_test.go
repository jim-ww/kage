package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// benchKeyNav moves the message selection up then down steps times each,
// rendering a frame after every key the way the program does.
func benchKeyNav(b *testing.B, nMsgs, steps int, view bool) {
	benchKeyNavSize(b, nMsgs, steps, view, 120, 40)
}

func benchKeyNavSize(b *testing.B, nMsgs, steps int, view bool, w, h int) {
	benchKeyNavModel(b, newMouseSweepBenchModel(30, nMsgs), steps, view, w, h)
}

func benchKeyNavModel(b *testing.B, m Model, steps int, view bool, w, h int) {
	m.width, m.termHeight = w, h
	m.updateSizes()
	m.refreshViewport()
	m.viewport.GotoBottom()
	_ = m.View()
	up := tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
	down := tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}
	b.ResetTimer()
	for b.Loop() {
		for _, k := range []tea.KeyPressMsg{up, down} {
			for range steps {
				next, _ := m.Update(k)
				m = next.(Model)
				if view {
					_ = m.View()
				}
			}
		}
	}
}

// BenchmarkKeyNavFullPipeline is a held ctrl+k/ctrl+j through a long chat,
// Update and View per key.
func BenchmarkKeyNavFullPipeline(b *testing.B) { benchKeyNav(b, 200, 40, true) }

// BenchmarkKeyNavUpdateOnly is BenchmarkKeyNavFullPipeline without View.
func BenchmarkKeyNavUpdateOnly(b *testing.B) { benchKeyNav(b, 200, 40, false) }

// BenchmarkKeyNavFullPipelineLarge is BenchmarkKeyNavFullPipeline on a
// large terminal.
func BenchmarkKeyNavFullPipelineLarge(b *testing.B) { benchKeyNavSize(b, 200, 40, true, 300, 80) }

// realisticBenchMessages is a chat mixing what renderMessage handles:
// both sides, multi-line text, emoji and CJK, links, code blocks, replies,
// reactions, edits and attachments.
func realisticBenchMessages(n int) []Message {
	bodies := []string{
		"ok",
		"hey 👋 how's it going? 日本語もOK",
		"check this out https://example.com/some/long/path?with=query&and=more",
		"```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```",
		strings.Repeat("a long message that wraps over several lines of the chat view, ", 6),
		"line one\nline two\nline three",
		"👩‍💻🇯🇵🎉 *bold* _italic_ `code`",
	}
	base := time.Now().Add(-time.Duration(n) * time.Minute)
	msgs := make([]Message, n)
	for i := range msgs {
		msg := Message{
			ID: fmt.Sprintf("m%d", i), StoreID: int64(i + 1), Author: "bob",
			Content: bodies[i%len(bodies)], SentAt: base.Add(time.Duration(i) * time.Minute),
			IsMe: i%3 == 0, Delivered: true, Encrypted: true, EncMethod: "omemo-v1",
		}
		if msg.IsMe {
			msg.Author = "me"
		}
		if i%5 == 4 && i > 0 {
			msg.ReplyToID = msgs[i-3].ID
		}
		if i%4 == 1 {
			msg.Reactions = []Reaction{{Emoji: "👍", Count: 2, Mine: true}, {Emoji: "😂", Count: 1}}
		}
		if i%11 == 7 {
			msg.Edited = true
		}
		if i%13 == 5 {
			msg.Attachments = []string{"https://example.com/upload/photo.jpg"}
			msg.Content = msg.Attachments[0]
		}
		msgs[i] = msg
	}
	return msgs
}

// BenchmarkKeyNavRealistic is BenchmarkKeyNavFullPipeline over
// realisticBenchMessages.
func BenchmarkKeyNavRealistic(b *testing.B) {
	m := newMouseSweepBenchModel(30, 1)
	m.accounts[0].Messages[0] = realisticBenchMessages(200)
	m.selectedMsg = 199
	benchKeyNavModel(b, m, 40, true, 120, 40)
}

// BenchmarkRefreshViewportRealistic is a full re-render of
// realisticBenchMessages, as paging and history loads do.
func BenchmarkRefreshViewportRealistic(b *testing.B) {
	m := newMouseSweepBenchModel(30, 1)
	m.accounts[0].Messages[0] = realisticBenchMessages(200)
	m.selectedMsg = 199
	for b.Loop() {
		m.refreshViewport()
	}
}
