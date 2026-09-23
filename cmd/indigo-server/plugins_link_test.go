package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A server started by an agent's --mcp, with no window's bridge to pass
// INDIGO_PLUGINS_DIR, used to run with no plugins — and every window joining
// it afterwards had none either. It must fall back to the link Attach keeps.
func TestUsePluginsLinkIfUnset(t *testing.T) {
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins-abc")
	if err := os.Mkdir(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(plugins, link); err != nil {
		t.Fatal(err)
	}

	t.Run("unset falls back to the link", func(t *testing.T) {
		t.Setenv("INDIGO_PLUGINS_DIR", "")
		usePluginsLinkIfUnset(link)
		if got := os.Getenv("INDIGO_PLUGINS_DIR"); got != link {
			t.Errorf("INDIGO_PLUGINS_DIR = %q, want %q", got, link)
		}
	})
	t.Run("an explicit value wins", func(t *testing.T) {
		t.Setenv("INDIGO_PLUGINS_DIR", "/from/the/bridge")
		usePluginsLinkIfUnset(link)
		if got := os.Getenv("INDIGO_PLUGINS_DIR"); got != "/from/the/bridge" {
			t.Errorf("INDIGO_PLUGINS_DIR = %q, want the bridge's value kept", got)
		}
	})
	t.Run("no link leaves it unset", func(t *testing.T) {
		t.Setenv("INDIGO_PLUGINS_DIR", "")
		usePluginsLinkIfUnset(filepath.Join(dir, "missing"))
		if got := os.Getenv("INDIGO_PLUGINS_DIR"); got != "" {
			t.Errorf("INDIGO_PLUGINS_DIR = %q, want unset", got)
		}
	})
}

// remoteEnv's null entries arrive as names in INDIGO_UNSET_ENV, because the
// host's `docker exec -e` cannot remove a variable the image defines. This
// process must remove them — and the carrier — before starting the daemon, so
// nothing it runs inherits the image's value.
func TestApplyRemoteEnvUnsets(t *testing.T) {
	t.Setenv("IMAGE_ONLY_A", "from the image")
	t.Setenv("IMAGE_ONLY_B", "from the image")
	t.Setenv("KEEP_ME", "kept")
	t.Setenv("INDIGO_UNSET_ENV", "IMAGE_ONLY_A,IMAGE_ONLY_B")

	applyRemoteEnvUnsets()

	for _, name := range []string{"IMAGE_ONLY_A", "IMAGE_ONLY_B", "INDIGO_UNSET_ENV"} {
		if v, ok := os.LookupEnv(name); ok {
			t.Errorf("%s still set (%q)", name, v)
		}
	}
	if os.Getenv("KEEP_ME") != "kept" {
		t.Error("a variable not listed was removed")
	}
}
