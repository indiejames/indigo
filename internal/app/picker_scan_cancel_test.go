package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

// TestClosingThePickerCancelsItsScan: a workspace scan whose picker has closed
// or been replaced has nobody to show its result to, so it is cancelled,
// which also stops the server's walk. Each case runs the scan's command after
// the picker went away, against a real server: a cancelled scan returns no
// files, where one still wanted lists the workspace.
func TestClosingThePickerCancelsItsScan(t *testing.T) {
	rpc, dir := sessionWorkspace(t, "a.go")
	cases := []struct {
		name   string
		then   tea.Msg
		cancel bool
	}{
		{"picker still open", nil, false},
		{"picker cancelled", pickerCancelledMsg{}, true},
		{"picker replaced", client.OpenPickerMsg{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := App{rpc: rpc, cfg: &config.Config{}, workDir: dir, width: 100, height: 24, fileChangedIdx: -1,
				buffers: []client.Model{newReloadTestModel(1, dir+"/x.go")}}
			a.picker = newFilePicker(dir, "", a.width, a.height, true)
			scan := a.startPickerFileScan(dir)
			if tc.then != nil {
				a, _ = step(t, a, tc.then)
			}
			got := scan().(pickerFilesMsg)
			if cancelled := len(got.files) == 0; cancelled != tc.cancel {
				t.Errorf("scan returned %v, want cancelled=%v", got.files, tc.cancel)
			}
			if tc.cancel && a.scan != nil && a.scan.picker != a.picker {
				t.Error("a scan for a closed picker is still recorded")
			}
		})
	}
}
