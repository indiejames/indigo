package agenttools

import "testing"

// TestSameExceptWhitespace: apply_edits does not retry, or record as lost, an
// edit that only changed whitespace, because format-on-save reverting one
// looks exactly like a lost save.
func TestSameExceptWhitespace(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"f(a, b)\n", "f(a,  b)\n", true},
		{"\tx := 1\n", "    x := 1\n\n", true},
		{"x := 1\n", "x := 2\n", false},
		{"ab", "a b", true}, // the price of ignoring whitespace: it cannot see this one
	} {
		if got := sameExceptWhitespace(tc.a, tc.b); got != tc.want {
			t.Errorf("sameExceptWhitespace(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
