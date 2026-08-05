package main

import (
	"reflect"
	"testing"
)

// TestRenderMsgCachesUnchangedContent verifies that renderMsg reuses a cached
// entry rather than re-rendering when content and width are unchanged. A
// sentinel value is seeded directly into mdRenderCache (not obtainable from a
// real glamour render), so the cache is the only source it could have come
// from.
func TestRenderMsgCachesUnchangedContent(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	sentinel := []string{"SENTINEL"}
	m.mdRenderCache[0] = mdCacheEntry{content: "hello world", width: 80, lines: sentinel}

	got := m.renderMsg(0, ConvMsg{Role: RoleAssistant, Content: "hello world"}, 80)

	if !reflect.DeepEqual(got, sentinel) {
		t.Errorf("renderMsg() = %v, want cached sentinel %v (cache was not reused)", got, sentinel)
	}
}

// TestRenderMsgInvalidatesOnContentChange verifies that a cache entry keyed
// on stale content (e.g. a streaming message that has since grown) is not
// reused once the content no longer matches.
func TestRenderMsgInvalidatesOnContentChange(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	sentinel := []string{"SENTINEL"}
	m.mdRenderCache[0] = mdCacheEntry{content: "partial", width: 80, lines: sentinel}

	got := m.renderMsg(0, ConvMsg{Role: RoleAssistant, Content: "partial and more"}, 80)

	if reflect.DeepEqual(got, sentinel) {
		t.Errorf("renderMsg() returned stale cached lines %v after content changed", sentinel)
	}
	if e := m.mdRenderCache[0]; e.content != "partial and more" {
		t.Errorf("mdRenderCache[0].content = %q, want %q (cache not refreshed)", e.content, "partial and more")
	}
}

// TestRenderMsgInvalidatesOnWidthChange verifies that a cache entry keyed on
// a stale terminal width (e.g. before a resize) is not reused once the
// requested width no longer matches.
func TestRenderMsgInvalidatesOnWidthChange(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	sentinel := []string{"SENTINEL"}
	m.mdRenderCache[0] = mdCacheEntry{content: "hello world", width: 80, lines: sentinel}

	got := m.renderMsg(0, ConvMsg{Role: RoleAssistant, Content: "hello world"}, 40)

	if reflect.DeepEqual(got, sentinel) {
		t.Errorf("renderMsg() returned stale cached lines %v after width changed", sentinel)
	}
	if e := m.mdRenderCache[0]; e.width != 40 {
		t.Errorf("mdRenderCache[0].width = %d, want 40 (cache not refreshed)", e.width)
	}
}

// TestClearResetsMdRenderCache verifies that /clear drops all cached render
// entries rather than leaving stale ones that a reused conversation index
// could pick up after the conversation is cleared.
func TestClearResetsMdRenderCache(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.mdRenderCache[0] = mdCacheEntry{content: "old", width: 80, lines: []string{"SENTINEL"}}

	m = submitText(m, "/clear")

	if len(m.mdRenderCache) != 0 {
		t.Errorf("mdRenderCache after /clear = %v, want empty", m.mdRenderCache)
	}

	// A message reusing conv index 0 post-clear must render fresh, not pick
	// up the pre-clear entry.
	got := m.renderMsg(0, ConvMsg{Role: RoleAssistant, Content: "new content"}, 80)
	if reflect.DeepEqual(got, []string{"SENTINEL"}) {
		t.Errorf("renderMsg() returned stale pre-clear cached lines after /clear")
	}
}
