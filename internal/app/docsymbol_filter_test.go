package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

// LSP SymbolKind numbers, for readability below.
const (
	kFunction  = 12
	kMethod    = 6
	kClass     = 5
	kInterface = 11
	kVariable  = 13
	kConstant  = 14
	kProperty  = 7
	kTypeParam = 26
)

// A file's symbols as a hierarchical server reports them after flattening:
// ContainerName is empty exactly for the top-level ones.
func sampleSymbols() []client.ClientSymbol {
	return []client.ClientSymbol{
		{Name: "handlers", Kind: kConstant},                       // top-level const map
		{Name: "get", Kind: kProperty, ContainerName: "handlers"}, // its property
		{Name: "routes", Kind: kVariable},                         // top-level array
		{Name: "run", Kind: kFunction},                            // top-level function
		{Name: "tmp", Kind: kVariable, ContainerName: "run"},      // local inside run
		{Name: "Server", Kind: kClass},                            // top-level class
		{Name: "start", Kind: kMethod, ContainerName: "Server"},   // its method
		{Name: "Options", Kind: kInterface},                       // top-level interface
		{Name: "T", Kind: kTypeParam, ContainerName: "Options"},   // a type parameter
	}
}

func names(syms []client.ClientSymbol) string {
	var out []string
	for _, s := range syms {
		out = append(out, s.Name)
	}
	return strings.Join(out, ",")
}

func TestSymbolCategory(t *testing.T) {
	for kind, want := range map[uint8]symCategory{
		kFunction: symCatFunctions, kMethod: symCatFunctions, 9: symCatFunctions,
		kClass: symCatTypes, kInterface: symCatTypes, 23: symCatTypes, 10: symCatTypes,
		kVariable: symCatVariables, kConstant: symCatVariables, 18: symCatVariables, 19: symCatVariables,
		kProperty: symCatMembers, 8: symCatMembers, 22: symCatMembers,
		kTypeParam: symCatOther, 25: symCatOther, 99: symCatOther,
	} {
		if got := symbolCategory(kind); got != want {
			t.Errorf("symbolCategory(%d) = %v, want %v", kind, got, want)
		}
	}
}

// The default is what the request asked for: top-level functions, classes,
// interfaces and types — and top-level maps and arrays — but nothing nested
// in a function, class or object.
func TestDocSymbolPickerDefaultShowsOnlyTopLevelDeclarations(t *testing.T) {
	p := newDocSymbolPicker(sampleSymbols(), defaultDocSymbolFilters(), 80, 24)
	if got, want := names(p.filtered), "handlers,Options,routes,run,Server"; got != want {
		t.Errorf("default list = %s, want %s", got, want)
	}
}

func TestDocSymbolPickerCheckboxes(t *testing.T) {
	p := newDocSymbolPicker(sampleSymbols(), defaultDocSymbolFilters(), 80, 24)

	p.filters.toggle(docSymFocusTopLevel) // show nested symbols too
	p.applyFilter("")
	if got, want := names(p.filtered), "handlers,Options,routes,run,Server,start,tmp"; got != want {
		t.Errorf("top-level off = %s, want %s (members and other still off)", got, want)
	}

	p.filters.toggle(docSymFocusCategory0 + int(symCatMembers))
	p.applyFilter("")
	if !strings.Contains(names(p.filtered), "get") {
		t.Errorf("members on, but property missing: %s", names(p.filtered))
	}

	p.filters.toggle(docSymFocusCategory0 + int(symCatFunctions)) // functions off
	p.applyFilter("")
	if got := names(p.filtered); strings.Contains(got, "run") || strings.Contains(got, "start") {
		t.Errorf("functions off, but a function or method is listed: %s", got)
	}

	// The text filter narrows within what the checkboxes allow.
	p.applyFilter("hand")
	if got := names(p.filtered); got != "handlers" {
		t.Errorf("text filter = %s, want handlers", got)
	}
}

// An empty list caused by the checkboxes says so, and how to change them.
func TestDocSymbolPickerExplainsHiddenMatches(t *testing.T) {
	p := newDocSymbolPicker(sampleSymbols(), defaultDocSymbolFilters(), 80, 24)
	p.applyFilter("tmp") // exists, but is a local
	if len(p.filtered) != 0 {
		t.Fatalf("a local was shown under the defaults: %s", names(p.filtered))
	}
	if out := p.render(); !strings.Contains(out, "1 hidden by filters") {
		t.Errorf("empty list does not say a match was hidden:\n%s", out)
	}
}

func pickerApp(syms []client.ClientSymbol) App {
	a := App{cfg: &config.Config{}, width: 80, height: 24}
	updated, _ := a.Update(client.OpenDocSymbolPickerMsg{Syms: syms})
	return updated.(App)
}

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func press(t *testing.T, a App, keys ...string) App {
	t.Helper()
	for _, k := range keys {
		updated, _ := a.handleDocSymbolPickerKey(key(k))
		a = updated.(App)
	}
	return a
}

func TestDocSymbolPickerKeys(t *testing.T) {
	a := pickerApp(sampleSymbols())

	// Tab to "Top-level only", Space toggles it: nested symbols appear.
	a = press(t, a, "tab", "space")
	if a.docSymbolPicker.filters.topLevelOnly {
		t.Fatal("Space on the focused Top-level box did not toggle it")
	}
	if !strings.Contains(names(a.docSymbolPicker.filtered), "tmp") {
		t.Errorf("nested symbols not shown after unchecking Top-level: %s", names(a.docSymbolPicker.filtered))
	}

	// Enter on a checkbox toggles it rather than jumping away.
	a = press(t, a, "enter")
	if a.docSymbolPicker == nil || !a.docSymbolPicker.filters.topLevelOnly {
		t.Fatal("Enter on a checkbox should toggle it, not close the picker")
	}

	// Shift+Tab back to the text box; typing goes there, space included.
	a = press(t, a, "shift+tab", "h", "a", "space")
	if got := a.docSymbolPicker.filter; got != "ha " {
		t.Errorf("filter text = %q, want %q (a space in the text box is text)", got, "ha ")
	}

	// Typing while a checkbox has focus returns focus to the text box.
	a = press(t, a, "tab", "x")
	if a.docSymbolPicker.focus != docSymFocusFilter || !strings.HasSuffix(a.docSymbolPicker.filter, "x") {
		t.Errorf("typing on a checkbox: focus=%d filter=%q", a.docSymbolPicker.focus, a.docSymbolPicker.filter)
	}
}

// The checkboxes are remembered when the picker is reopened in the session.
func TestDocSymbolPickerRemembersFilters(t *testing.T) {
	a := pickerApp(sampleSymbols())
	a = press(t, a, "tab", "space", "esc") // uncheck Top-level, close
	if a.docSymbolPicker != nil {
		t.Fatal("Esc did not close the picker")
	}
	updated, _ := a.Update(client.OpenDocSymbolPickerMsg{Syms: sampleSymbols()})
	a = updated.(App)
	if a.docSymbolPicker.filters.topLevelOnly {
		t.Error("reopened picker reset Top-level only to the default")
	}
}

// The boxes wrap to fit instead of overflowing the picker at its narrowest.
func TestDocSymbolPickerCheckboxesFitNarrowWidth(t *testing.T) {
	p := newDocSymbolPicker(sampleSymbols(), defaultDocSymbolFilters(), 40, 24)
	out := p.render()
	// Each box must sit whole on one row. Rendering at a fixed width wraps
	// overflow by itself — at spaces, which can leave "[x]" on one row and its
	// label on the next.
	for _, label := range append([]string{"Top-level only"}, symCategoryLabels[:]...) {
		if !strings.Contains(out, "[x] "+label) && !strings.Contains(out, "[ ] "+label) {
			t.Errorf("checkbox %q is not whole on one row:\n%s", label, out)
		}
	}
	widths := map[int]bool{}
	for _, line := range strings.Split(out, "\n") {
		widths[lipgloss.Width(line)] = true
	}
	if len(widths) != 1 {
		t.Errorf("picker rows have differing widths %v — a checkbox row overflowed", widths)
	}
}

// Symbols are listed alphabetically — case-insensitively, so capitalised type
// names are not all bunched ahead of lower-case functions — with a repeated
// name (an overload, or one name declared twice) in file order. Filtering
// keeps that order, and the caller's slice is not reordered.
func TestDocSymbolPickerSortsAlphabetically(t *testing.T) {
	in := []client.ClientSymbol{
		{Name: "zeta", Kind: kFunction, Line: 1},
		{Name: "Beta", Kind: kClass, Line: 2},
		{Name: "alpha", Kind: kFunction, Line: 30},
		{Name: "Alpha", Kind: kFunction, Line: 10}, // ties with "alpha": earlier line first
		{Name: "gamma", Kind: kConstant, Line: 5},
	}
	p := newDocSymbolPicker(in, defaultDocSymbolFilters(), 80, 24)
	if got, want := names(p.filtered), "Alpha,alpha,Beta,gamma,zeta"; got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
	p.applyFilter("a") // every name above contains an "a"
	if got, want := names(p.filtered), "Alpha,alpha,Beta,gamma,zeta"; got != want {
		t.Errorf("filtered order = %s, want %s", got, want)
	}
	if in[0].Name != "zeta" {
		t.Error("the caller's symbol slice was reordered")
	}
}
