package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/config"
)

// TestPasteReachesModals is a regression test for the same Bubble Tea v1 → v2
// gap covered in internal/client's paste_test.go, on the App side: every
// modal here is driven by `msg.(tea.KeyMsg)` routing, and v2's paste arrives
// as a separate tea.PasteMsg that matches none of it. Pasting into any of
// these dialogs silently did nothing after the migration.
func TestPasteReachesModals(t *testing.T) {
	cfg := &config.Config{}

	t.Run("file picker", func(t *testing.T) {
		a := App{width: 80, height: 24, cfg: cfg, fileChangedIdx: -1,
			picker: newFilePicker("/tmp", "/tmp", 80, 24, true)}
		got, _ := a.Update(tea.PasteMsg{Content: "main.go"})
		if q := got.(App).picker.query; !strings.Contains(q, "main.go") {
			t.Errorf("picker query = %q, want it to contain the pasted text", q)
		}
	})

	t.Run("plugin input", func(t *testing.T) {
		a := App{width: 80, height: 24, cfg: cfg, fileChangedIdx: -1,
			pluginInput: &appPluginInput{title: "t", width: 80, height: 24}}
		got, _ := a.Update(tea.PasteMsg{Content: "hello"})
		if txt := got.(App).pluginInput.text; txt != "hello" {
			t.Errorf("plugin input text = %q, want %q", txt, "hello")
		}
	})

	t.Run("new file input", func(t *testing.T) {
		a := App{width: 80, height: 24, cfg: cfg, fileChangedIdx: -1,
			newFileInput: &appPluginInput{title: "New File", width: 80, height: 24}}
		got, _ := a.Update(tea.PasteMsg{Content: "pkg/new.go"})
		if txt := got.(App).newFileInput.text; txt != "pkg/new.go" {
			t.Errorf("new-file input text = %q, want %q", txt, "pkg/new.go")
		}
	})

	t.Run("search & replace", func(t *testing.T) {
		d := newSearchReplaceDialog("/tmp", 80, 24)
		d.setFocus(sraFocusSearch)
		a := App{width: 80, height: 24, cfg: cfg, fileChangedIdx: -1, searchReplace: d}
		got, _ := a.Update(tea.PasteMsg{Content: "needle"})
		if v := got.(App).searchReplace.searchInput.Value(); v != "needle" {
			t.Errorf("search input = %q, want %q", v, "needle")
		}
	})

	t.Run("empty paste is a no-op", func(t *testing.T) {
		a := App{width: 80, height: 24, cfg: cfg, fileChangedIdx: -1,
			pluginInput: &appPluginInput{title: "t", width: 80, height: 24, text: "keep"}}
		got, _ := a.Update(tea.PasteMsg{Content: ""})
		if txt := got.(App).pluginInput.text; txt != "keep" {
			t.Errorf("plugin input text = %q, want it unchanged", txt)
		}
	})
}
