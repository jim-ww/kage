package xmpp

import (
	"encoding/xml"
	"slices"
	"strings"
	"testing"
)

const legacySample = `<storage xmlns='storage:bookmarks'>
<conference name='Council' autojoin='true' jid='council@chat.example.org/x'><nick>me</nick><password>pw</password></conference>
<conference jid='quiet@chat.example.org' autojoin='0'/>
<url name='Site' url='https://example.org'/>
</storage>`

func TestLegacyBookmarks(t *testing.T) {
	var s legacyStorage
	if err := xml.Unmarshal([]byte(legacySample), &s); err != nil {
		t.Fatal(err)
	}
	want := []Bookmark{
		{Room: "council@chat.example.org", Name: "Council", Nick: "me", Password: "pw", Autojoin: true},
		{Room: "quiet@chat.example.org"},
	}
	if got := legacyBookmarks(s); !slices.Equal(got, want) {
		t.Fatalf("bookmarks = %+v", got)
	}

	// Dropping one keeps the rest, the url bookmark too.
	s.Elements = slices.DeleteFunc(s.Elements, func(e legacyElem) bool { return e.conferenceRoom() == "council@chat.example.org" })
	raw, err := xml.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back legacyStorage
	if err := xml.Unmarshal(raw, &back); err != nil {
		t.Fatalf("written storage %s: %v", raw, err)
	}
	if got := legacyBookmarks(back); !slices.Equal(got, want[1:]) {
		t.Errorf("after removal %+v", got)
	}
	if !strings.Contains(string(raw), "https://example.org") {
		t.Errorf("url bookmark lost: %s", raw)
	}
}
