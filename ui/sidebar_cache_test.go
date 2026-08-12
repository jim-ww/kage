package ui

import "testing"

// TestRenderSidebarCachesIdenticalInputs guards renderSidebar's cache: two
// calls with byte-identical inputs must return the exact same string
// (verifying the cache path actually returns something, not just that it
// doesn't crash) and skip recomputation — checked indirectly by mutating
// the cache's rendered field directly to a sentinel and confirming a
// repeat call with the same inputs returns that sentinel instead of
// re-rendering.
func TestRenderSidebarCachesIdenticalInputs(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	entry := sidebarCacheEntry{
		statusLine: "status",
		body:       "chat list body",
		width:      20,
		height:     10,
		boxWidth:   22,
		boxHeight:  12,
		border:     m.styles.colors.borderD,
	}

	first := m.renderSidebar(entry)
	if first == "" {
		t.Fatal("renderSidebar returned empty output")
	}

	// Poison the cache's stored output so a real cache hit is unmistakable.
	m.sidebarRenderCache.rendered = "SENTINEL"

	second := m.renderSidebar(entry)
	if second != "SENTINEL" {
		t.Fatalf("expected a cache hit (SENTINEL) on identical inputs, got a fresh render: %q", second)
	}
}

// TestRenderSidebarInvalidatesOnBodyChange guards against the cache going
// stale: a change to any single input (here, the chat-list body — the
// thing that changes on every incoming message, unread count, or
// selection move) must not return the stale cached output.
func TestRenderSidebarInvalidatesOnBodyChange(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	base := sidebarCacheEntry{
		statusLine: "status",
		body:       "chat list body v1",
		width:      20,
		height:     10,
		boxWidth:   22,
		boxHeight:  12,
		border:     m.styles.colors.borderD,
	}
	first := m.renderSidebar(base)

	changed := base
	changed.body = "chat list body v2"
	second := m.renderSidebar(changed)

	if second == first {
		t.Fatal("renderSidebar returned stale cached output after the body changed")
	}

	// And a repeat of the *new* inputs should now hit the (updated) cache.
	m.sidebarRenderCache.rendered = "SENTINEL2"
	third := m.renderSidebar(changed)
	if third != "SENTINEL2" {
		t.Fatalf("expected the cache to have updated to the new inputs, got a fresh render: %q", third)
	}
}

// TestRenderSidebarInvalidatesOnEachField guards the remaining cache-key
// fields individually — a bug that compared only some of them (e.g.
// forgetting boxHeight) would silently show a stale box size/border after
// a resize or view-focus change.
func TestRenderSidebarInvalidatesOnEachField(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	base := sidebarCacheEntry{
		statusLine: "status",
		body:       "body",
		width:      20,
		height:     10,
		boxWidth:   22,
		boxHeight:  12,
		border:     m.styles.colors.borderD,
	}
	baseline := m.renderSidebar(base)

	variants := map[string]sidebarCacheEntry{
		"statusLine": {statusLine: "other", body: base.body, width: base.width, height: base.height, boxWidth: base.boxWidth, boxHeight: base.boxHeight, border: base.border},
		"width":      {statusLine: base.statusLine, body: base.body, width: base.width + 1, height: base.height, boxWidth: base.boxWidth, boxHeight: base.boxHeight, border: base.border},
		"height":     {statusLine: base.statusLine, body: base.body, width: base.width, height: base.height + 1, boxWidth: base.boxWidth, boxHeight: base.boxHeight, border: base.border},
		"boxWidth":   {statusLine: base.statusLine, body: base.body, width: base.width, height: base.height, boxWidth: base.boxWidth + 1, boxHeight: base.boxHeight, border: base.border},
		"boxHeight":  {statusLine: base.statusLine, body: base.body, width: base.width, height: base.height, boxWidth: base.boxWidth, boxHeight: base.boxHeight + 1, border: base.border},
		"border":     {statusLine: base.statusLine, body: base.body, width: base.width, height: base.height, boxWidth: base.boxWidth, boxHeight: base.boxHeight, border: m.styles.colors.borderA},
	}

	for name, v := range variants {
		// Reset to a known baseline (and a poisoned sentinel) before each
		// variant so a false cache hit is unmistakable regardless of order.
		m.renderSidebar(base)
		m.sidebarRenderCache.rendered = "SENTINEL-" + name
		got := m.renderSidebar(v)
		if got == "SENTINEL-"+name {
			t.Fatalf("field %q: cache hit on a changed field (stale render)", name)
		}
		_ = baseline
	}
}
