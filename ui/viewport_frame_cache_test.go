package ui

import "testing"

// TestRenderViewportFrameCachesIdenticalInputs guards renderViewportFrame's
// cache the same way TestRenderSidebarCachesIdenticalInputs guards
// renderSidebar's: a cache hit must actually return the cached value.
func TestRenderViewportFrameCachesIdenticalInputs(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	entry := viewportFrameCacheEntry{width: 40, height: 10, content: "message content"}

	first := m.renderViewportFrame(entry)
	if first == "" {
		t.Fatal("renderViewportFrame returned empty output")
	}

	m.viewportFrameCache.rendered = "SENTINEL"
	second := m.renderViewportFrame(entry)
	if second != "SENTINEL" {
		t.Fatalf("expected a cache hit (SENTINEL) on identical inputs, got a fresh render: %q", second)
	}
}

// TestRenderViewportFrameInvalidatesOnEachField guards against a stale
// frame after a resize or scroll/selection change — each field individually
// must invalidate the cache.
func TestRenderViewportFrameInvalidatesOnEachField(t *testing.T) {
	m := newTestModelWithSender(&fakeSuccessSender{}, nil)
	base := viewportFrameCacheEntry{width: 40, height: 10, content: "content"}

	variants := map[string]viewportFrameCacheEntry{
		"width":   {width: base.width + 1, height: base.height, content: base.content},
		"height":  {width: base.width, height: base.height + 1, content: base.content},
		"content": {width: base.width, height: base.height, content: "different content"},
	}

	for name, v := range variants {
		m.renderViewportFrame(base)
		m.viewportFrameCache.rendered = "SENTINEL-" + name
		got := m.renderViewportFrame(v)
		if got == "SENTINEL-"+name {
			t.Fatalf("field %q: cache hit on a changed field (stale render)", name)
		}
	}
}
