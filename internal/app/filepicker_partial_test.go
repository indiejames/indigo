package app

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/config"
)

// TestPickerSaysWhenItsListIsPartial: searching a partial list can miss a
// file that exists, so the search title says the list is partial, and says
// nothing for a full one.
func TestPickerSaysWhenItsListIsPartial(t *testing.T) {
	for _, partial := range []bool{false, true} {
		a := App{cfg: &config.Config{}, fileChangedIdx: -1, picker: newFilePicker("/tmp", "/tmp", 100, 24, true)}
		a, _ = step(t, a, pickerFilesMsg{seq: a.picker.seq, files: []string{"a.go", "b.go"}, partial: partial})
		a.picker.setQuery("a")
		if got := strings.Contains(a.picker.View(), "partial: 2 files listed"); got != partial {
			t.Errorf("partial=%v: title mentions a partial list = %v", partial, got)
		}
	}
}
