package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePlugin creates an installed-plugin directory: a manifest declaring
// binaries per platform, and whichever of those files actually exist.
func writePlugin(t *testing.T, root, name string, declared map[string]string, present []string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("name = \"" + name + "\"\n[binaries]\n")
	for k, v := range declared {
		b.WriteString("\"" + k + "\" = \"" + v + "\"\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range present {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("binary "+f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStagePluginsTakesOnlyTheContainersPlatform(t *testing.T) {
	host := t.TempDir()
	writePlugin(t, host, "bookmarks", map[string]string{
		"darwin/arm64": "bookmarks-darwin-arm64",
		"linux/arm64":  "bookmarks-linux-arm64",
		"linux/amd64":  "bookmarks-linux-amd64",
	}, []string{"bookmarks-darwin-arm64", "bookmarks-linux-arm64", "bookmarks-linux-amd64"})

	staged, err := StagePlugins(host, "linux", "arm64")
	if err != nil {
		t.Fatalf("StagePlugins: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(staged.Dir) }) //nolint:errcheck

	entries, err := os.ReadDir(filepath.Join(staged.Dir, "bookmarks"))
	if err != nil {
		t.Fatalf("staged dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	// The manifest plus one binary. Carrying all four platforms would be most
	// of the transfer for none of the benefit.
	if len(names) != 2 {
		t.Fatalf("staged %v, want plugin.toml and one binary", names)
	}
	for _, n := range names {
		if strings.Contains(n, "darwin") || strings.Contains(n, "amd64") {
			t.Errorf("staged %q, which the container cannot run", n)
		}
	}
}

// TestStagePluginsSkipsWhatItCannotCarry: a plugin built only for the host is a
// normal thing to have installed, and refusing to open an editor over it would
// be absurd. It has to be *reported*, though — silently missing plugins is the
// behaviour this whole change exists to fix.
func TestStagePluginsSkipsWhatItCannotCarry(t *testing.T) {
	host := t.TempDir()
	writePlugin(t, host, "portable", map[string]string{"linux/arm64": "portable-linux-arm64"},
		[]string{"portable-linux-arm64"})
	writePlugin(t, host, "hostonly", map[string]string{"darwin/arm64": "hostonly-darwin-arm64"},
		[]string{"hostonly-darwin-arm64"})
	// Declared for linux but never built — the state every bundled plugin was
	// in before `make build-plugins-linux` existed.
	writePlugin(t, host, "declared-not-built", map[string]string{"linux/arm64": "declared-not-built-linux-arm64"},
		nil)

	staged, err := StagePlugins(host, "linux", "arm64")
	if err != nil {
		t.Fatalf("StagePlugins: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(staged.Dir) }) //nolint:errcheck

	if strings.Join(staged.Staged, ",") != "portable" {
		t.Errorf("staged = %v, want [portable]", staged.Staged)
	}
	if len(staged.Skipped) != 2 {
		t.Errorf("skipped = %v, want the two that cannot run there", staged.Skipped)
	}
	msg := staged.Describe()
	for _, want := range []string{"hostonly", "declared-not-built"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Describe() = %q, want it to name %q", msg, want)
		}
	}
}

// TestStagePluginsHashesContent is what stops an upgraded plugin being skipped
// as "already there" — the same trap the server binary fell into.
func TestStagePluginsHashesContent(t *testing.T) {
	mk := func(content string) string {
		host := t.TempDir()
		dir := filepath.Join(host, "p")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plugin.toml"),
			[]byte("name = \"p\"\n[binaries]\n\"linux/arm64\" = \"p-linux-arm64\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "p-linux-arm64"), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		staged, err := StagePlugins(host, "linux", "arm64")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(staged.Dir) }) //nolint:errcheck
		return staged.Hash
	}
	if mk("build one") == mk("build two") {
		t.Error("two builds hashed the same; an upgraded plugin would never be copied")
	}
	first, second := mk("same"), mk("same")
	if first != second {
		t.Errorf("identical builds hashed differently (%s vs %s); plugins would be re-copied every attach",
			first, second)
	}
}

func TestStagePluginsWithNothingInstalled(t *testing.T) {
	staged, err := StagePlugins(filepath.Join(t.TempDir(), "absent"), "linux", "arm64")
	if err != nil {
		t.Fatalf("a missing plugins directory should not be an error: %v", err)
	}
	if staged.Dir != "" || len(staged.Staged) != 0 {
		t.Errorf("staged = %+v, want nothing", staged)
	}
}

// TestStagedPluginsAreReadableByAnyContainerUser is the regression for plugins
// silently not loading in a devcontainer.
//
// os.MkdirTemp creates 0700 and `docker cp` preserves the source mode, so the
// staged directory arrived readable only by root — while the server runs as the
// image's remoteUser. It could not even list the directory, and because that
// error was discarded upstream the symptom was simply no plugins, which is
// indistinguishable from having none installed.
func TestStagedPluginsAreReadableByAnyContainerUser(t *testing.T) {
	host := t.TempDir()
	writePlugin(t, host, "p", map[string]string{"linux/arm64": "p-linux-arm64"}, []string{"p-linux-arm64"})

	staged, err := StagePlugins(host, "linux", "arm64")
	if err != nil {
		t.Fatalf("StagePlugins: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(staged.Dir) }) //nolint:errcheck

	info, err := os.Stat(staged.Dir)
	if err != nil {
		t.Fatal(err)
	}
	// Traversable and listable by others, or a non-root server cannot reach in.
	if mode := info.Mode().Perm(); mode&0o055 != 0o055 {
		t.Errorf("staging dir mode = %04o, want world-readable and traversable", mode)
	}

	// And so must everything under it.
	err = filepath.WalkDir(staged.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() && info.Mode().Perm()&0o055 != 0o055 {
			t.Errorf("%s mode = %04o, want world-readable and traversable", path, info.Mode().Perm())
		}
		if !d.IsDir() && info.Mode().Perm()&0o004 == 0 {
			t.Errorf("%s mode = %04o, want world-readable", path, info.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
