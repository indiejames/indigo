package client

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

const testFileForCursor = `package x

import "testing"

func helper() int {
	return 1
}

func TestAlpha(t *testing.T) {
	x := helper()
	_ = x
}

func (s *Suite) TestMethod() {
}

func BenchmarkBeta(b *testing.B) {
	for b.Loop() {
	}
}
`

// The enclosing test is found from anywhere inside it — its first line, its
// body, its closing brace — and nowhere else.
func TestTestAtCursor(t *testing.T) {
	m := newTestModel(testFileForCursor)
	m.filePath = "/w/x/x_test.go"
	for _, tc := range []struct {
		line int
		want string
	}{
		{0, ""},           // package clause
		{5, ""},           // inside a helper
		{8, "TestAlpha"},  // the func line
		{9, "TestAlpha"},  // the body
		{11, "TestAlpha"}, // the closing brace
		{12, ""},          // the blank line after it
		{14, ""},          // a suite method: go test -run cannot select it
		{18, "BenchmarkBeta"},
	} {
		m.cursor = document.Pos{Line: tc.line}
		if got := m.testAtCursor(); got != tc.want {
			t.Errorf("line %d: got %q, want %q", tc.line+1, got, tc.want)
		}
	}
	m.filePath = "/w/x/x.go"
	m.cursor = document.Pos{Line: 9}
	if got := m.testAtCursor(); got != "" {
		t.Errorf("a non-test file gave %q", got)
	}
}

func TestTestArgs(t *testing.T) {
	if got, want := testArgs("TestAlpha"), []string{"-test.run", "^TestAlpha$"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, want := testArgs("BenchmarkBeta"), []string{"-test.run", "^$", "-test.bench", "^BenchmarkBeta$"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDebugTestOutsideATest(t *testing.T) {
	m := newTestModel(testFileForCursor)
	m.filePath = "/w/x/x_test.go"
	m.cursor = document.Pos{Line: 5}
	updated, cmd := executeDebugTest(m)
	if cmd != nil || !strings.Contains(updated.(Model).status, "not in a Go test") {
		t.Errorf("status %q, cmd %v", updated.(Model).status, cmd != nil)
	}
}

func key1(m Model, k string) Model {
	updated, _ := m.Update(tea.KeyPressMsg{Code: []rune(k)[0], Text: k})
	return updated.(Model)
}

// The configuration list opens as a menu when it arrives; a key picks one and
// starts it, Esc closes it.
func TestDebugConfigurationsMenu(t *testing.T) {
	m := newTestModel("a\n")
	m.bufID = 3
	cfgs := []DebugConfig{{Name: "server", Program: "/w/cmd/server"}, {Name: "unit", Mode: "test", Program: "/w/pkg"}}
	m = m.handleDebugConfigs(debugConfigsMsg{bufID: 3, configs: cfgs})
	node, ok := m.resolveCommand(m.prefixSeq)
	if !ok || len(node.children) != 2 {
		t.Fatalf("menu not open: seq %v", m.prefixSeq)
	}
	if node.children[0].key != "1" || node.children[1].label != "unit (test)" {
		t.Errorf("menu items = %+v", node.children)
	}
	if !strings.Contains(strings.Join(viewLines(m), "\n"), "server") {
		t.Error("the menu is not drawn")
	}
	picked := key1(m, "1")
	if len(picked.prefixSeq) != 0 || !strings.Contains(picked.status, "Starting debugger for server") {
		t.Errorf("after 1: seq %v, status %q", picked.prefixSeq, picked.status)
	}
	closed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if len(closed.(Model).prefixSeq) != 0 {
		t.Error("Esc did not close the menu")
	}
}

// A list that comes back late must not grab the keyboard from whatever the
// user started meanwhile, or land in another buffer.
func TestDebugConfigurationsArrivingLate(t *testing.T) {
	cfgs := []DebugConfig{{Name: "server"}}
	m := newTestModel("a\n")
	m.bufID = 3
	if got := m.handleDebugConfigs(debugConfigsMsg{bufID: 4, configs: cfgs}); len(got.prefixSeq) != 0 {
		t.Error("another buffer's list opened the menu")
	}
	m.mode = ModeInsert
	if got := m.handleDebugConfigs(debugConfigsMsg{bufID: 3, configs: cfgs}); len(got.prefixSeq) != 0 {
		t.Error("the menu opened in Insert mode")
	}
	m.mode = ModeNormal
	m.prefixSeq = []string{"g"}
	if got := m.handleDebugConfigs(debugConfigsMsg{bufID: 3, configs: cfgs}); !reflect.DeepEqual(got.prefixSeq, []string{"g"}) {
		t.Errorf("the menu replaced a prefix in progress: %v", got.prefixSeq)
	}
	m.prefixSeq = nil
	if got := m.handleDebugConfigs(debugConfigsMsg{bufID: 3}); !strings.Contains(got.status, ".indigo/debug.toml") {
		t.Errorf("empty list status = %q, want where to add configurations", got.status)
	}
}
