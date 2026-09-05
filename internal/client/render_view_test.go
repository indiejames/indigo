package client

import (
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestFileTypeNameFilenameStyleKeys verifies extensionless, filename-style
// language keys (matched by base filename rather than extension — see
// internal/highlight's registerLang calls for "dockerfile" and the
// gitcommit family, and docs/language-support.md) get a proper display
// name instead of falling through to the raw registry key.
func TestFileTypeNameFilenameStyleKeys(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"Dockerfile", "Dockerfile"},
		{"/some/project/Dockerfile", "Dockerfile"},
		{".git/COMMIT_EDITMSG", "Git Commit"},
	}
	for _, c := range cases {
		if got := fileTypeName(c.path); got != c.want {
			t.Errorf("fileTypeName(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// TestFileTypeNameForKeyFilenameStyleKeys is TestFileTypeNameFilenameStyleKeys
// for the ":set ft=<key>" override path (a bare registry key, not a path).
func TestFileTypeNameForKeyFilenameStyleKeys(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"dockerfile", "Dockerfile"},
		{"DOCKERFILE", "Dockerfile"}, // case-insensitive
		{"commit_editmsg", "Git Commit"},
		{"not-a-real-language", "not-a-real-language"}, // unmapped: raw key fallback preserved
	}
	for _, c := range cases {
		if got := fileTypeNameForKey(c.key); got != c.want {
			t.Errorf("fileTypeNameForKey(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestTruncatePathFrontPreservesFilename verifies the status bar's file-path
// segment truncates from the front (leading "…") rather than the back, so
// the filename — the part that actually identifies the buffer — stays
// visible even when the containing directories don't fit. Regression test
// for the file-path segment previously using truncateCenter (back-truncation
// despite the name), which could ellipsize the filename itself away.
func TestTruncatePathFrontPreservesFilename(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		suffix string
		width  int
		want   string
	}{
		{"fits as-is", "internal/client/render_view.go", "", 40, "internal/client/render_view.go"},
		{"fits with suffix", "main.go", " [+]", 20, "main.go [+]"},
		{
			"truncates directories, keeps full filename",
			"/Volumes/Data/Golang/indigo/internal/client/render_view.go",
			"",
			20,
			"…ient/render_view.go",
		},
		{
			"keeps full filename with dirty suffix",
			"/Volumes/Data/Golang/indigo/internal/client/render_view.go",
			" [+]",
			25,
			"…lient/render_view.go [+]",
		},
		{
			"filename alone doesn't fit: truncates filename, no leading ellipsis prefix noise",
			"/a/b/c/a-very-long-filename-indeed.go",
			"",
			10,
			"a-very-lo…",
		},
		{"width zero", "main.go", "", 0, ""},
		{"width smaller than suffix falls back to back-truncation", "main.go", " [+]", 2, "m…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := truncatePathFront(c.path, c.suffix, c.width); got != c.want {
				t.Errorf("truncatePathFront(%q, %q, %d) = %q, want %q", c.path, c.suffix, c.width, got, c.want)
			}
		})
	}
}

// TestTruncatePathFrontWideCharactersStayWithinBudget is a regression test
// for measuring width in runes instead of terminal display cells: CJK
// characters and other wide runes occupy two cells each, so a rune-counting
// truncation can let the rendered path overflow its allocated status-bar
// width even though the rune count looks like it fits. Asserts the actual
// invariant (rendered width never exceeds the budget) rather than an exact
// string, since hand-counting double-width runes is error-prone and not the
// point of the test.
func TestTruncatePathFrontWideCharactersStayWithinBudget(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		suffix string
		width  int
	}{
		{"wide directories, ascii filename", "/项目/文件夹/源代码/render_view.go", "", 20},
		{"wide directories with dirty suffix", "/项目/文件夹/源代码/render_view.go", " [+]", 20},
		{"wide filename itself too long", "/a/b/项目源代码文件非常长的名字.go", "", 12},
		{"emoji in filename", "/Users/jamie/notes/🎉celebration-plans🎉.md", "", 18},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncatePathFront(c.path, c.suffix, c.width)
			if w := lipgloss.Width(got); w > c.width {
				t.Errorf("truncatePathFront(%q, %q, %d) = %q, rendered width %d exceeds budget %d",
					c.path, c.suffix, c.width, got, w, c.width)
			}
			base := filepath.Base(c.path)
			if lipgloss.Width(base)+lipgloss.Width(c.suffix) <= c.width && !strings.Contains(got, base) {
				t.Errorf("truncatePathFront(%q, %q, %d) = %q, want full basename %q preserved (budget allows it)",
					c.path, c.suffix, c.width, got, base)
			}
		})
	}
}
