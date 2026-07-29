package client

import (
	"sort"
	"strings"
)

// completionScore ranks how well a completion matches the typed prefix; lower is
// better, and -1 means no match. An empty prefix matches everything at score 0,
// preserving the server's own sortText ordering (e.g. member lists after '.').
func completionScore(item ClientCompletion, lowerPrefix string) int {
	if lowerPrefix == "" {
		return 0
	}
	text := item.FilterText
	if text == "" {
		text = item.Label
	}
	lt := strings.ToLower(text)
	switch {
	case strings.HasPrefix(lt, lowerPrefix):
		return 0
	case strings.Contains(lt, lowerPrefix):
		return 1
	case fuzzyMatch(lowerPrefix, lt):
		return 2
	default:
		return -1
	}
}

// filterCompletions keeps items matching prefix and orders them by match
// quality, then the server's sortText, then label. Servers sort auto-import
// candidates last via sortText (tsserver prefixes theirs with U+FFFF), so
// without this filtering step they never surface past the visible window. The
// result is uncapped; the renderer shows a window into it.
func filterCompletions(items []ClientCompletion, prefix string) []ClientCompletion {
	lower := strings.ToLower(prefix)
	type scored struct {
		item  ClientCompletion
		score int
	}
	matched := make([]scored, 0, len(items))
	for _, it := range items {
		s := completionScore(it, lower)
		if s < 0 {
			continue
		}
		matched = append(matched, scored{it, s})
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].score != matched[j].score {
			return matched[i].score < matched[j].score
		}
		if matched[i].item.SortText != matched[j].item.SortText {
			return matched[i].item.SortText < matched[j].item.SortText
		}
		return matched[i].item.Label < matched[j].item.Label
	})
	out := make([]ClientCompletion, len(matched))
	for i := range matched {
		out[i] = matched[i].item
	}
	return out
}
