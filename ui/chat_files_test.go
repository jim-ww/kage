package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// filesAndLoad is a fake daemon listing files with ChatFiles and loading
// history windows with a fakeHistoryLoader.
type filesAndLoad struct {
	*fakeHistoryLoader
	files []Message
}

func (f filesAndLoad) ChatFiles(accountIdx int, to string) tea.Cmd {
	return func() tea.Msg { return ChatFilesMsg{AccountIdx: accountIdx, From: to, Messages: f.files} }
}

func filesOf(m Model) *searchResultsBody {
	if md := m.modals.Find(chatFilesModalID); md != nil {
		return md.Body.(*searchResultsBody)
	}
	return nil
}

// TestChatFilesTabListsAttachmentsAndJumps guards the search's Files tab:
// enter there opens the files list busy, ChatFilesMsg fills it with one row
// per attachment, oldest first with the cursor on the newest, each with its name, size and date, the
// filter narrows by name, and picking a row jumps the chat to its message.
func TestChatFilesTabListsAttachmentsAndJumps(t *testing.T) {
	AttachmentsDir = t.TempDir()
	t.Cleanup(func() { AttachmentsDir = "" })
	day := time.Date(2025, time.March, 4, 10, 0, 0, 0, time.Local)
	files := []Message{ // oldest first, as ChatFiles lists them
		{ID: "a", StoreID: 1, SentAt: day, Attachments: []string{"https://up.example.test/1/report.pdf"}},
		{ID: "b", StoreID: 2, SentAt: day.Add(time.Hour), IsMe: true, Attachments: []string{"https://up.example.test/2/cat.png", "https://up.example.test/3/dog%20pic.jpg"}},
	}
	loader := &fakeHistoryLoader{fakeSuccessSender: &fakeSuccessSender{}, response: HistoryWindowMsg{Messages: files}}
	m := newTestModelWithMessages(files[:1], filesAndLoad{loader, files})
	m.width, m.termHeight = 120, 30
	m.updateSizes()
	m.attachmentSizes = map[string]int64{files[0].Attachments[0]: 2048}
	m.attachmentSizeFailed = map[string]bool{files[1].Attachments[0]: true, files[1].Attachments[1]: true}

	m = run(t, m, tea.KeyPressMsg{Code: '/', Mod: tea.ModCtrl})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	next, cmd := m.Update(keyText("enter"))
	m = next.(Model)
	if modalTitle(m) != "Files" || filesOf(m) == nil || filesOf(m).status == "" {
		t.Fatalf("enter on the Files tab showed %q, want the files list busy", modalTitle(m))
	}
	result := nonIdleCmd(cmd)
	if _, ok := result.(ChatFilesMsg); !ok {
		t.Fatalf("enter on the Files tab ran %T, want ChatFiles", result)
	}
	m, _, _ = m.handleEventMsg(result)

	body := filesOf(m)
	if len(body.shown) != 3 {
		t.Fatalf("listed %d files, want one per attachment (3)", len(body.shown))
	}
	rows := make([]string, len(body.Items))
	for i, it := range body.Items {
		rows[i] = it.Label
	}
	for i, want := range []string{"report.pdf", "cat.png", "dog pic.jpg"} {
		if !strings.Contains(rows[i], want) {
			t.Fatalf("row %d = %q, want %q (oldest first)", i, rows[i], want)
		}
	}
	if body.Cursor() != 2 {
		t.Fatalf("cursor on row %d, want the newest (2)", body.Cursor())
	}
	if !strings.Contains(rows[0], "2.0 KB") || !strings.Contains(rows[0], "4 Mar 2025") {
		t.Fatalf("row %q lacks its size or date", rows[0])
	}

	body.filter = "reprot"
	body.relist()
	if len(body.shown) != 1 || !strings.Contains(body.Items[0].Label, "report.pdf") {
		t.Fatalf("filter by name listed %v, want report.pdf alone", body.shown)
	}

	next, pick := m.Update(keyText("enter"))
	m = next.(Model)
	if filesOf(m) != nil {
		t.Fatal("files list still open after picking a file")
	}
	if nonIdleCmd(pick) == nil || loader.lastAnchor == nil || loader.lastAnchor.StoreID != 1 {
		t.Fatalf("picking report.pdf anchored on %+v, want its message (StoreID 1)", loader.lastAnchor)
	}
}

// TestChatFilesLongListWorksInView guards a files list over a huge history:
// only the rows in view are labelled and have their sizes asked for, and
// scrolling asks for the next ones.
func TestChatFilesLongListWorksInView(t *testing.T) {
	AttachmentsDir = t.TempDir()
	t.Cleanup(func() { AttachmentsDir = "" })
	files := make([]Message, 5000)
	for i := range files {
		files[i] = Message{StoreID: int64(i + 1), SentAt: time.Unix(int64(1e9+i), 0), Attachments: []string{fmt.Sprintf("https://up.example.test/%d/f.bin", i)}}
	}
	m := newTestModelWithMessages(nil, filesAndLoad{&fakeHistoryLoader{fakeSuccessSender: &fakeSuccessSender{}}, files})
	m.width, m.termHeight = 120, 30
	m.updateSizes()
	m.openChatFiles("")
	m, cmd, _ := m.handleEventMsg(ChatFilesMsg{AccountIdx: 0, From: "bob@example.test", Messages: files})

	body := filesOf(m)
	start, end := body.Window()
	if end-start >= 100 || end-start == 0 {
		t.Fatalf("window = %d..%d, want a screenful", start, end)
	}
	if end != len(files) || body.Items[start-1].Label != "" {
		t.Fatal("list didn't open on the newest rows, or a row out of view got labelled")
	}
	want, ok := cmd().(chatFileSizesMsg)
	if !ok || len(want.targets) != end-start {
		t.Fatalf("asked for sizes of %d files, want the %d in view", len(want.targets), end-start)
	}

	for range end - start {
		m = run(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	}
	body = filesOf(m)
	if newStart, _ := body.Window(); newStart == start || body.Items[newStart].Label == "" {
		t.Fatal("scrolling up didn't label the rows coming into view")
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = next.(Model)
	if _, ok := nonIdleCmd(cmd).(chatFileSizesMsg); !ok {
		t.Fatal("scrolling to new rows didn't ask for their sizes")
	}
}
