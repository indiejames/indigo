//go:build lang_gitcommit || lang_all

package highlight

import "testing"

var gitCommitSample = []byte(`
# Please enter the commit message for your changes. Lines starting
# with '#' will be ignored, and an empty message aborts the commit.
#
# On branch main
# Your branch is up to date with 'origin/main'.
#
# Changes to be committed:
#	modified:   README.md
#
# Untracked files:
#	test.md
#
`)

func TestGitCommitHighlighterCreated(t *testing.T) {
	for _, path := range []string{".git/COMMIT_EDITMSG", ".git/MERGE_MSG", ".git/SQUASH_MSG", ".git/TAG_EDITMSG"} {
		if h := New(path); h == nil {
			t.Errorf("New(%q) = nil, want Highlighter", path)
		}
	}
}

// winnerAt mirrors renderLineRunes' spanIdxAt: the first (highest-priority
// after sorting) span whose range covers col.
func winnerAt(t *testing.T, spans []Span, col int) Span {
	t.Helper()
	for _, s := range spans {
		if col >= s.StartCol && col < s.EndCol {
			return s
		}
	}
	t.Fatalf("no span covers col %d", col)
	return Span{}
}

// TestGitCommitNestedCapturesWinOverEnclosingComment is a regression test:
// gitcommit's grammar nests structured captures (branch name, status
// keyword, filepath, section heading) inside a git status hint line that is
// itself entirely wrapped in a (generated_comment) node carrying @comment.
// @comment has the highest priority in the default capture table (so a
// plain comment reads as fully commented-out), but here it must lose to the
// narrower, more specific capture nested inside it — otherwise every git
// status hint (branch name, "modified:", filenames, section headings)
// renders as flat comment-green instead of the differentiated colors Helix
// and other editors show for these commit-template hints.
func TestGitCommitNestedCapturesWinOverEnclosingComment(t *testing.T) {
	h := NewForKey("commit_editmsg")
	if h == nil {
		t.Fatal("no highlighter registered for commit_editmsg")
	}
	spans := h.Highlight(gitCommitSample)

	commentANSI, _, ok := captureANSI("comment")
	if !ok {
		t.Fatal("captureTable has no \"comment\" entry — test assumption invalid")
	}

	cases := []struct {
		name      string
		line, col int
		wantScope string
	}{
		{"branch name (On branch main)", 4, 12, "markup.link"},
		{"branch name (origin/main)", 5, 35, "markup.link"},
		{"section heading", 7, 3, "markup.heading"},
		{"status keyword (modified:)", 8, 3, "keyword"},
		{"changed filepath", 8, 15, "string.special.path"},
		{"untracked filepath", 11, 3, "string.special.path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want, _, ok := captureANSI(c.wantScope)
			if !ok {
				t.Fatalf("captureTable has no %q entry — test assumption invalid", c.wantScope)
			}
			got := winnerAt(t, spans[c.line], c.col)
			if got.ANSI != want {
				if got.ANSI == commentANSI {
					t.Errorf("line %d col %d: rendered as plain @comment, want @%s", c.line, c.col, c.wantScope)
				} else {
					t.Errorf("line %d col %d: ansi = %q, want @%s's %q", c.line, c.col, got.ANSI, c.wantScope, want)
				}
			}
		})
	}

	// Plain comment prose (no nested capture) must still render as @comment.
	t.Run("plain comment prose", func(t *testing.T) {
		got := winnerAt(t, spans[1], 3)
		if got.ANSI != commentANSI {
			t.Errorf("line 1 col 3: ansi = %q, want @comment's %q", got.ANSI, commentANSI)
		}
	})
}
