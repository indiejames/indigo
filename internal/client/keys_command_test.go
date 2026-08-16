package client

import "testing"

func TestParseGrepArgs(t *testing.T) {
	cases := []struct {
		in                        string
		pattern, include, exclude string
	}{
		{"TODO", "TODO", "", ""},
		{"TODO *.go", "TODO", "*.go", ""},
		{"TODO !vendor/", "TODO", "", "vendor/"},
		{"TODO *.go !vendor/", "TODO", "*.go", "vendor/"},
		{"TODO *.go *.ts !vendor/ !**/*_test.go", "TODO", "*.go *.ts", "vendor/ **/*_test.go"},
		{"foo bar", "foo bar", "", ""},
		{"*.go", "", "*.go", ""},
		{"!vendor/", "", "", "vendor/"},
		{"", "", "", ""},
	}
	for _, c := range cases {
		pattern, include, exclude := parseGrepArgs(c.in)
		if pattern != c.pattern || include != c.include || exclude != c.exclude {
			t.Errorf("parseGrepArgs(%q) = (%q, %q, %q), want (%q, %q, %q)",
				c.in, pattern, include, exclude, c.pattern, c.include, c.exclude)
		}
	}
}
