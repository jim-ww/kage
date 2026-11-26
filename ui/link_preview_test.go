package ui

import (
	"bytes"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// linkPreviewModel is newTestModelWithMessages with link previews on (New
// sets them from DisplayOptions, off) and an empty preview store.
func linkPreviewModel(t *testing.T, msgs []Message) Model {
	t.Helper()
	withMediaCache(t)
	m := newTestModelWithMessages(nil, &stubDaemon{})
	reset := func() {
		linkPreviewMu.Lock()
		linkPreviews, linkPreviewPending, linkPreviewQueue = map[string]linkPagePreview{}, map[string]bool{}, nil
		linkPreviewMu.Unlock()
	}
	reset()
	setLinkPreviewsEnabled(true)
	t.Cleanup(func() { setLinkPreviewsEnabled(false); reset() })
	m.accounts[0].Messages[0] = msgs
	m.refreshViewport()
	return m
}

// linkSite serves a page with OpenGraph tags and its picture.
func linkSite(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var png bytes.Buffer
	if err := encodePNG(&png, image.NewRGBA(image.Rect(0, 0, 120, 63))); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/article":
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			rw.Write([]byte(`<!doctype html><html><head>
<title>fallback title</title>
<meta property="og:site_name" content="Example News">
<meta property="og:title" content="Kage ships   previews">
<meta name="description" content="plain description">
<meta property="og:description" content="Half-block pictures in the terminal.">
<meta property="og:image" content="/pic.png">
</head><body><meta property="og:title" content="ignored"></body></html>`))
		case "/pic.png", "/direct.png":
			rw.Header().Set("Content-Type", "image/png")
			rw.Write(png.Bytes())
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestParsePageMeta(t *testing.T) {
	got := parsePageMeta([]byte(`<html><head><title> Doc  Title </title>
<meta name="description" content="desc">
<meta name="twitter:image" content="https://cdn.example.test/t.jpg">
</head><body></body></html>`))
	want := pageMeta{title: "Doc Title", description: "desc", image: "https://cdn.example.test/t.jpg"}
	if got != want {
		t.Fatalf("parsePageMeta = %+v, want %+v", got, want)
	}
}

// TestLinkPreviewCard: a message's link is fetched once, and its card -
// site, title, description and picture - drawn under it.
func TestLinkPreviewCard(t *testing.T) {
	srv, hits := linkSite(t)
	body := "look " + srv.URL + "/article and " + srv.URL + "/other"
	m := linkPreviewModel(t, []Message{{ID: "a", Content: body}})
	before := len(m.viewportLines)
	m = runBatch(t, m, takeMediaThumbRequests())

	view := ansi.Strip(strings.Join(m.viewportLines, "\n"))
	for _, want := range []string{"Example News · Kage ships previews", "Half-block pictures in the terminal.", upperHalfBlock} {
		if !strings.Contains(view, want) {
			t.Fatalf("card lacks %q:\n%s", want, view)
		}
	}
	if len(m.viewportLines) <= before+2 {
		t.Fatal("card has no picture rows")
	}
	if strings.Contains(view, "ignored") || strings.Contains(view, "fallback title") {
		t.Fatalf("card took a fallback or a body tag over the head's OpenGraph:\n%s", view)
	}
	m.refreshViewport()
	if takeMediaThumbRequests() != nil || hits.Load() != 2 {
		t.Fatalf("%d requests (want page + picture), or more queued", hits.Load())
	}
}

// TestLinkPreviewDirectImage: a link straight to a picture shows it.
func TestLinkPreviewDirectImage(t *testing.T) {
	srv, _ := linkSite(t)
	m := linkPreviewModel(t, []Message{{ID: "a", Content: srv.URL + "/direct.png"}})
	m = runBatch(t, m, takeMediaThumbRequests())
	if !strings.Contains(strings.Join(m.viewportLines, "\n"), upperHalfBlock) {
		t.Fatal("linked image not drawn")
	}
}

// TestLinkPreviewOffByDefault: nothing is fetched unless link_previews is
// set.
func TestLinkPreviewOffByDefault(t *testing.T) {
	withMediaCache(t)
	srv, hits := linkSite(t)
	_ = newTestModelWithMessages([]Message{{ID: "a", Content: srv.URL + "/article"}}, &stubDaemon{})
	if takeMediaThumbRequests() != nil || hits.Load() != 0 {
		t.Fatal("link fetched with link previews off")
	}
}

// TestLinkPreviewFailureNotRetried: a link with nothing to show gets no
// card and isn't fetched again on every render.
func TestLinkPreviewFailureNotRetried(t *testing.T) {
	srv, hits := linkSite(t)
	m := linkPreviewModel(t, []Message{{ID: "a", Content: srv.URL + "/missing"}})
	before := len(m.viewportLines)
	m = runBatch(t, m, takeMediaThumbRequests())
	m.refreshViewport()
	if takeMediaThumbRequests() != nil || hits.Load() != 1 || len(m.viewportLines) != before {
		t.Fatalf("hits=%d lines %d→%d", hits.Load(), before, len(m.viewportLines))
	}
}

// TestPublicOnlyDial: a link can't reach this machine or its network.
func TestPublicOnlyDial(t *testing.T) {
	allowLoopbackLinks = false
	t.Cleanup(func() { allowLoopbackLinks = true })
	for _, addr := range []string{"127.0.0.1:80", "[::1]:443", "10.1.2.3:80", "192.168.1.1:80", "172.16.0.5:80", "169.254.169.254:80", "[fe80::1]:80", "[fd00::1]:80", "0.0.0.0:80"} {
		if publicOnlyDial("tcp", addr, nil) == nil {
			t.Errorf("%s allowed", addr)
		}
	}
	for _, addr := range []string{"1.1.1.1:443", "[2606:4700::1111]:443"} {
		if err := publicOnlyDial("tcp", addr, nil); err != nil {
			t.Errorf("%s refused: %v", addr, err)
		}
	}
}
