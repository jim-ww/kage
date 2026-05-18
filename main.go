package main

import (
	"fmt"
	"os"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"codeberg.org/jim-ww/kage/config"
	"codeberg.org/jim-ww/kage/ui"
)

func main() {
	const myName = "me"
	chatItemsA := []list.Item{
		ui.Chat{Name: "Emma", LastMessage: "see you at the meeting"},
		ui.Chat{Name: "Lucas", LastMessage: "almost there"},
		ui.Chat{Name: "Olivia", LastMessage: "thanks, looks good"},
		ui.Chat{Name: "Ethan", LastMessage: "production is green"},
		ui.Chat{Name: "Sophia", LastMessage: "call at 5?"},
	}
	chatItemsB := []list.Item{
		ui.Chat{Name: "#ops", LastMessage: "deploy wrapped cleanly"},
		ui.Chat{Name: "#infra", LastMessage: "cpu spike is gone"},
		ui.Chat{Name: "Nina", LastMessage: "pushed the patch"},
	}
	chatItemsC := []list.Item{
		ui.Chat{Name: "#friends", LastMessage: "dinner on friday?"},
		ui.Chat{Name: "Mom", LastMessage: "call when free"},
	}

	now := time.Now()
	messagesA := map[int][]ui.Message{
		0: { // Emma - made longer
			{Author: "Emma", Content: "hey! are we still meeting at 3?", SentAt: now.Add(-50 * time.Minute), IsMe: false},
			{Author: myName, Content: "yes, I'll be there", SentAt: now.Add(-48 * time.Minute), IsMe: true},
			{Author: "Emma", Content: "great — bringing the docs", SentAt: now.Add(-47 * time.Minute), IsMe: false},
			{Author: myName, Content: "perfect", SentAt: now.Add(-46 * time.Minute), IsMe: true},
			{Author: "Emma", Content: "should I prepare the slides too?", SentAt: now.Add(-45 * time.Minute), IsMe: false},
			{Author: myName, Content: "yes please, that would help", SentAt: now.Add(-44 * time.Minute), IsMe: true},
			{Author: "Emma", Content: "okay, see you soon", SentAt: now.Add(-43 * time.Minute), IsMe: false},
		},
		1: { // Lucas - made longer
			{Author: "Lucas", Content: "stuck in traffic, 10 min late", SentAt: now.Add(-20 * time.Minute), IsMe: false},
			{Author: myName, Content: "no problem, take your time", SentAt: now.Add(-19 * time.Minute), IsMe: true},
			{Author: "Lucas", Content: "almost there now", SentAt: now.Add(-8 * time.Minute), IsMe: false},
			{Author: myName, Content: "cool, I'll wait at the lobby", SentAt: now.Add(-7 * time.Minute), IsMe: true},
			{Author: "Lucas", Content: "thanks!", SentAt: now.Add(-6 * time.Minute), IsMe: false},
		},
		2: { // Olivia - made longer
			{Author: "Olivia", Content: "can you upload the report to Drive?", SentAt: now.Add(-2 * time.Hour), IsMe: false},
			{Author: myName, Content: "uploaded it to the project folder", SentAt: now.Add(-119 * time.Minute), IsMe: true},
			{Author: "Olivia", Content: "perfect, thanks!", SentAt: now.Add(-118 * time.Minute), IsMe: false},
			{Author: myName, Content: "let me know if you need anything else", SentAt: now.Add(-117 * time.Minute), IsMe: true},
			{Author: "Olivia", Content: "actually, can you also share the raw data?", SentAt: now.Add(-116 * time.Minute), IsMe: false},
			{Author: myName, Content: "sure, uploading now", SentAt: now.Add(-115 * time.Minute), IsMe: true},
			{Author: "Olivia", Content: "you're the best!", SentAt: now.Add(-114 * time.Minute), IsMe: false},
		},
		3: {
			{Author: "Ethan", Content: "that fix worked — production is green", SentAt: now.Add(-6 * time.Hour), IsMe: false},
			{Author: myName, Content: "awesome, good job", SentAt: now.Add(-355 * time.Minute), IsMe: true},
		},
		4: {
			{Author: "Sophia", Content: "free for a quick call later?", SentAt: now.Add(-35 * time.Minute), IsMe: false},
			{Author: myName, Content: "yes, ping me at 5pm", SentAt: now.Add(-34 * time.Minute), IsMe: true},
		},
	}
	messagesB := map[int][]ui.Message{
		0: {
			{Author: "drew", Content: "deploy wrapped cleanly", SentAt: now.Add(-12 * time.Minute), IsMe: false},
			{Author: myName, Content: "nice, keeping an eye on error rate", SentAt: now.Add(-10 * time.Minute), IsMe: true},
		},
		1: {
			{Author: "sre-bot", Content: "cpu spike resolved after cache warmup", SentAt: now.Add(-45 * time.Minute), IsMe: false},
			{Author: myName, Content: "good, closing the incident", SentAt: now.Add(-42 * time.Minute), IsMe: true},
		},
		2: {
			{Author: "Nina", Content: "pushed the patch, can you review later?", SentAt: now.Add(-90 * time.Minute), IsMe: false},
		},
	}
	messagesC := map[int][]ui.Message{
		0: {
			{Author: "Alex", Content: "dinner on friday?", SentAt: now.Add(-5 * time.Hour), IsMe: false},
			{Author: myName, Content: "works for me", SentAt: now.Add(-4 * time.Hour), IsMe: true},
		},
		1: {
			{Author: "Mom", Content: "call when free", SentAt: now.Add(-26 * time.Hour), IsMe: false},
		},
	}

	uiCfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	accounts := []ui.Account{
		{Name: "user@server", Chats: chatItemsA, Messages: messagesA},
		{Name: "work@irc", Chats: chatItemsB, Messages: messagesB},
		{Name: "social@chat", Chats: chatItemsC, Messages: messagesC},
	}

	model := ui.New(accounts, uiCfg.KeyMap, uiCfg.Theme)
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
