package highlight

import "testing"

// TestDotenvVariantsHighlightLikeDotenv is the report: .env was highlighted,
// but .env.local and its kind fell back to plain text, because their last
// extension (.local) names no language.
func TestDotenvVariantsHighlightLikeDotenv(t *testing.T) {
	if _, ok := langRegistry[".env"]; !ok {
		t.Skip("no shell grammar in this build")
	}
	for _, p := range []string{".env", "/w/.env.local", "/w/.ENV.Production", "/w/api/.env.example", "/w/prod.env"} {
		if got := lookupKey(p); got != ".env" {
			t.Errorf("lookupKey(%q) = %q, want .env", p, got)
		}
		if New(p) == nil {
			t.Errorf("New(%q) has no highlighter", p)
		}
	}
	if _, ok := langRegistry[".json"]; ok {
		if got := lookupKey("/w/.env.json"); got != ".json" {
			t.Errorf("lookupKey(.env.json) = %q, want the real extension to win", got)
		}
	}
	for _, p := range []string{"/w/.envrc.local", "/w/notes.local", "/w/.environment"} {
		if got := lookupKey(p); got == ".env" {
			t.Errorf("lookupKey(%q) = .env, want it left alone", p)
		}
	}
}
