package document

import "testing"

func TestNormalizeCRLF(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		crlf           bool
	}{
		{"all CRLF is converted", "a\r\nb\r\n", "a\nb\n", true},
		{"CRLF without a final newline", "a\r\nb", "a\nb", true},
		{"LF is untouched", "a\nb\n", "a\nb\n", false},
		// Mixed endings are left exactly as found, so a save cannot rewrite
		// every LF-only line; the stray \r is the renderer's to show.
		{"mixed is untouched", "a\r\nb\n", "a\r\nb\n", false},
		{"a lone CR is not a line ending", "a\rb\n", "a\rb\n", false},
		{"empty", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, crlf := NormalizeCRLF(tc.in)
			if got != tc.want || crlf != tc.crlf {
				t.Errorf("NormalizeCRLF(%q) = %q, %v; want %q, %v", tc.in, got, crlf, tc.want, tc.crlf)
			}
		})
	}
}

// Loading and saving an unedited CRLF file must reproduce it byte for byte.
func TestCRLFRoundTrip(t *testing.T) {
	for _, in := range []string{"a\r\nb\r\n", "a\r\n\r\nb", "x\ny\n", "a\r\nb\n"} {
		norm, crlf := NormalizeCRLF(in)
		if out := RestoreCRLF(norm, crlf); out != in {
			t.Errorf("round trip of %q gave %q", in, out)
		}
	}
}
