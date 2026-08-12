package app

import (
	"path/filepath"
	"testing"

	"github.com/indiejames/indigo/internal/client"
)

// TestActiveFileDir covers the picker's directory-of-active-file logic used
// when the file picker is opened from within a client (ctrl+p while editing).
func TestActiveFileDir(t *testing.T) {
	workDir := "/workspace/project"

	newModelAt := func(absPath string) client.Model {
		return client.New(&client.RPC{}, 1, "", 0, absPath, workDir, nil, false, 0)
	}

	t.Run("no buffers", func(t *testing.T) {
		a := App{workDir: workDir}
		if got := a.activeFileDir(); got != "" {
			t.Errorf("activeFileDir() = %q, want %q", got, "")
		}
	})

	t.Run("file in subdirectory", func(t *testing.T) {
		a := App{
			workDir: workDir,
			buffers: []client.Model{newModelAt(filepath.Join(workDir, "internal/app/app.go"))},
		}
		want := filepath.Join("internal", "app")
		if got := a.activeFileDir(); got != want {
			t.Errorf("activeFileDir() = %q, want %q", got, want)
		}
	})

	t.Run("file at project root", func(t *testing.T) {
		a := App{
			workDir: workDir,
			buffers: []client.Model{newModelAt(filepath.Join(workDir, "README.md"))},
		}
		if got := a.activeFileDir(); got != "" {
			t.Errorf("activeFileDir() = %q, want %q", got, "")
		}
	})

	t.Run("file outside workDir", func(t *testing.T) {
		a := App{
			workDir: workDir,
			buffers: []client.Model{newModelAt("/etc/hosts")},
		}
		if got := a.activeFileDir(); got != "" {
			t.Errorf("activeFileDir() = %q, want %q", got, "")
		}
	})

	t.Run("active buffer other than index 0", func(t *testing.T) {
		a := App{
			workDir: workDir,
			active:  1,
			buffers: []client.Model{
				newModelAt(filepath.Join(workDir, "README.md")),
				newModelAt(filepath.Join(workDir, "internal/client/model.go")),
			},
		}
		want := filepath.Join("internal", "client")
		if got := a.activeFileDir(); got != want {
			t.Errorf("activeFileDir() = %q, want %q", got, want)
		}
	})
}
