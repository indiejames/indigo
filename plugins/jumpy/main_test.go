package main

import (
	"context"
	"testing"

	"github.com/indiejames/indigo/internal/plugin"
	"github.com/indiejames/indigo/internal/plugin/plugintest"
	"github.com/indiejames/indigo/sdk"
)

func TestComputeTargetsLabelsEveryWordStart(t *testing.T) {
	got := computeTargets([]string{"foo bar", "  x_y.z", "", "héllo wörld"}, 10)
	want := []target{
		{"aa", 10, 0}, {"as", 10, 4}, // foo, bar
		{"ad", 11, 2}, {"af", 11, 6}, // x_y (one word), z after punctuation
		{"aj", 13, 0}, {"ak", 13, 6}, // columns count runes, not bytes
	}
	if len(got) != len(want) {
		t.Fatalf("targets = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("target %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestComputeTargetsStopsWhenLabelsRunOut(t *testing.T) {
	n := len(labelChars)
	line := ""
	for range n*n + 5 {
		line += "w "
	}
	got := computeTargets([]string{line}, 0)
	if len(got) != n*n {
		t.Fatalf("%d targets, want %d (every two-character label once)", len(got), n*n)
	}
	seen := map[string]bool{}
	for _, tg := range got {
		if seen[tg.label] {
			t.Fatalf("label %q used twice", tg.label)
		}
		seen[tg.label] = true
	}
}

// session returns a Jumpy mid-session over "foo bar baz" on line 3, without an
// editor: captured and decorations only read the state trigger left.
func session() *Jumpy {
	return &Jumpy{active: true, bufID: 1, clientID: 2, targets: computeTargets([]string{"foo bar baz"}, 3)}
}

func TestCapturedJumpsToTheTypedLabel(t *testing.T) {
	j := session()
	if r := j.captured("a"); !r.Handled || r.CaptureKeys != 1 {
		t.Fatalf("first char: %+v, want handled and one more key", r)
	}
	r := j.captured("s") // "as" is the second target, bar
	if !r.Handled || !r.HasCursor || r.CursorLine != 3 || r.CursorCol != 4 || r.CaptureKeys != 0 {
		t.Fatalf("second char: %+v, want a jump to 3:4 ending capture", r)
	}
	if j.active {
		t.Error("session still active after the jump")
	}
}

func TestCapturedCancels(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
	}{
		{"esc", []string{"esc"}},
		{"a named key", []string{"ctrl+c"}},
		{"a first char that is no label's", []string{"1"}},
		{"a first char no target starts with", []string{"z"}},
		{"a label that does not exist", []string{"a", "z"}},
		{"esc after the first char", []string{"a", "esc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := session()
			var r sdk.KeyResponse
			for _, k := range tc.keys {
				r = j.captured(k)
			}
			if !r.Handled || r.HasCursor || r.CaptureKeys != 0 {
				t.Errorf("%q: %+v, want handled, no jump, capture over", tc.keys, r)
			}
			if j.active {
				t.Errorf("%q left the session active", tc.keys)
			}
		})
	}
}

// TestCapturedRefusesANonLabelSecondChar is the regression test for the second
// label character being narrowed with byte(c) unchecked: U+0161 (š) became
// 0x61, 'a', so typing "aš" jumped to label "aa".
func TestCapturedRefusesANonLabelSecondChar(t *testing.T) {
	j := session()
	j.captured("a")
	if r := j.captured("š"); r.HasCursor {
		t.Fatalf("a then š jumped to %d:%d; only a label character may complete a label", r.CursorLine, r.CursorCol)
	}
}

func TestCapturedWaitsForTargetsNotYetComputed(t *testing.T) {
	j := &Jumpy{active: true}
	if r := j.captured("a"); !r.Handled || r.CaptureKeys != 2 || !j.active {
		t.Errorf("%+v (active %v), want the session kept waiting for both keys", r, j.active)
	}
}

func TestDecorationsShowLabelsThenTheirSecondChar(t *testing.T) {
	j := session()
	if d := j.decorations(1, 9, sdk.Range{}); d != nil {
		t.Errorf("another client's window got %+v", d)
	}
	if d := j.decorations(5, 2, sdk.Range{}); d != nil {
		t.Errorf("another buffer got %+v", d)
	}
	d := j.decorations(1, 2, sdk.Range{})
	if len(d) != 3 || d[1].Text != "as" || d[1].Line != 3 || d[1].Col != 4 || d[1].Kind != sdk.DecorationOverlay {
		t.Fatalf("decorations = %+v, want three overlays, the second \"as\" at 3:4", d)
	}
	j.captured("a")
	if d := j.decorations(1, 2, sdk.Range{}); len(d) != 3 || d[0].Text != "a" || d[1].Text != "s" {
		t.Errorf("after typing a: %+v, want each label's second character", d)
	}
}

// TestJumpyEndToEnd drives the built plugin through the editor's own path: the
// Command-menu item from plugin.toml starts a session, the labels arrive as
// decorations for the visible lines, and two captured keys jump.
func TestJumpyEndToEnd(t *testing.T) {
	bridge := plugintest.NewBridge()
	bridge.SetBuffer(1, []string{"skip me", "alpha beta", "gamma"})
	bridge.SetVisible(7, 1, 2)
	m := plugintest.Start(t, ".", bridge)
	ctx := context.Background()

	var item *plugin.MenuItem
	for _, it := range m.AllMenuItems() {
		if it.Label == "Jumpy" {
			item = &it
		}
	}
	if item == nil {
		t.Fatalf("no Jumpy menu item in %+v", m.AllMenuItems())
	}
	handled, _, _, _, _, capture, err := m.InvokeMenuAction(ctx, item.PluginName, item.Command, 1, 7, 0, 0)
	if err != nil || !handled || capture != 2 {
		t.Fatalf("menu action: handled %v, capture %d, err %v; want a session capturing two keys", handled, capture, err)
	}
	bridge.WaitForMessage(t, "jumpy: activated — type label chars")

	decs := m.GetDecorations(ctx, 7, 1, 1, 2)
	var labels []string
	for _, d := range decs {
		labels = append(labels, d.Text)
	}
	if len(decs) != 3 || decs[1].Text != "as" || decs[1].Line != 1 || decs[1].Col != 6 {
		t.Fatalf("decorations = %+v, want aa/as/ad over alpha, beta, gamma", decs)
	}

	for _, key := range []string{"a"} {
		if _, _, _, _, _, capture, err := m.HandleKey(ctx, key, "capture", 1, 7, 0, 0); err != nil || capture != 1 {
			t.Fatalf("capture %q: capture %d, err %v; want one more key", key, capture, err)
		}
	}
	_, _, line, col, hasCursor, capture, err := m.HandleKey(ctx, "s", "capture", 1, 7, 0, 0)
	if err != nil || !hasCursor || line != 1 || col != 6 || capture != 0 {
		t.Fatalf("label as: cursor %v %d:%d, capture %d, err %v; want a jump to beta at 1:6", hasCursor, line, col, capture, err)
	}
	if decs := m.GetDecorations(ctx, 7, 1, 1, 2); len(decs) != 0 {
		t.Errorf("labels still drawn after the jump: %+v (were %q)", decs, labels)
	}
}
