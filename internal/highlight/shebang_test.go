package highlight

import "testing"

func TestShebangKey(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"direct sh", "#!/bin/sh\necho hi\n", "sh"},
		{"direct bash", "#!/bin/bash\necho hi\n", "sh"},
		{"env bash", "#!/usr/bin/env bash\necho hi\n", "sh"},
		{"env python3", "#!/usr/bin/env python3\nprint('hi')\n", "py"},
		{"direct python2", "#!/usr/bin/python2\n", "py"},
		{"env node", "#!/usr/bin/env node\n", "js"},
		{"env ruby", "#!/usr/bin/env ruby\n", "rb"},
		{"env php with version", "#!/usr/bin/env php8\n", "php"},
		{"env with -S flag", "#!/usr/bin/env -S bash -euo pipefail\n", "sh"},
		{"first line only, later lines ignored", "#!/bin/sh\n#!/usr/bin/env python3\n", "sh"},
		{"trailing CR", "#!/bin/sh\r\necho hi\n", "sh"},
		{"whole content is just the shebang line, no trailing newline", "#!/bin/sh", "sh"},
		{"unknown interpreter", "#!/usr/bin/env perl\n", ""},
		{"env with no interpreter after flags", "#!/usr/bin/env -S\n", ""},
		{"no shebang, plain text", "hello world\n", ""},
		{"no shebang, looks like a comment", "# not a shebang\n", ""},
		{"empty content", "", ""},
		{"bare #!", "#!\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShebangKey(c.content); got != c.want {
				t.Errorf("ShebangKey(%q) = %q, want %q", c.content, got, c.want)
			}
		})
	}
}
