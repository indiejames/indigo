package client

import "testing"

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
