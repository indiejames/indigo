package container

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An agent's MCP registration names the server by one path for good, but
// ServerPath moves with every build. Attach must point the stable link at the
// server it is about to start, whether it copied it or found it present.
func TestAttachLinksTheStableServerPath(t *testing.T) {
	for _, exists := range []bool{false, true} {
		rt := &fakeRuntime{arch: "arm64", exists: exists}
		locate, _, hash := locateReal(t, "a server binary")
		remote := ServerPath("arm64", hash)

		stream, err := Attach(context.Background(), rt, "c1", "/workspaces/proj", AttachOptions{Locate: locate})
		if err != nil {
			t.Fatalf("Attach (exists=%v): %v", exists, err)
		}
		stream.Close() //nolint:errcheck

		env := strings.Join(rt.ranEnv, " ")
		if !strings.Contains(env, "INDIGO_TARGET="+remote) || !strings.Contains(env, "INDIGO_LINK="+StableServerLink) {
			t.Errorf("exists=%v: no link update to %s found in run env %q", exists, remote, env)
		}
	}
}

// A failed link is only a warning: the editor never uses it, so it must not
// stop the server starting.
func TestAttachSurvivesAFailedStableLink(t *testing.T) {
	rt := &fakeRuntime{arch: "arm64", exists: true, runErr: errors.New("read-only /tmp")}
	locate, _, _ := locateReal(t, "a server binary")
	var warned []string
	stream, err := Attach(context.Background(), rt, "c1", "/workspaces/proj", AttachOptions{
		Locate: locate,
		Warn:   func(msg string) { warned = append(warned, msg) },
	})
	if err != nil {
		t.Fatalf("Attach failed over the stable link: %v", err)
	}
	stream.Close() //nolint:errcheck
	if !strings.Contains(strings.Join(warned, "\n"), StableServerLink) {
		t.Errorf("warnings = %q, want one naming %s", warned, StableServerLink)
	}
}

// The link script itself, run for real: it must replace an existing link
// (an upgrade) rather than create a new one inside the old target.
func TestStableLinkScriptReplacesAnOlderLink(t *testing.T) {
	dir := t.TempDir()
	oldSrv, newSrv := filepath.Join(dir, "srv-old"), filepath.Join(dir, "srv-new")
	for _, p := range []string{oldSrv, newSrv} {
		if err := os.WriteFile(p, []byte(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(oldSrv, link); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shellForTest, "-c", stableLinkScript)
	cmd.Env = append(os.Environ(), "INDIGO_TARGET="+newSrv, "INDIGO_LINK="+link)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("link script: %v\n%s", err, out)
	}
	if got, err := os.Readlink(link); err != nil || got != newSrv {
		t.Errorf("link -> %q (%v), want %q", got, err, newSrv)
	}
}

// The plugins link is what a server started without a window (an agent's
// --mcp) falls back to. It must point at the plugins this attach carried in,
// and be removed when there are none, so the two ways of starting a server
// cannot disagree about which plugins it has.
func TestAttachLinksTheStablePluginsPath(t *testing.T) {
	t.Run("plugins carried in", func(t *testing.T) {
		rt := &fakeRuntime{arch: "arm64", exists: true}
		locate, _, _ := locateReal(t, "a server binary")
		stream, err := Attach(context.Background(), rt, "c1", "/w", AttachOptions{
			Locate: locate, PluginsDir: t.TempDir(), PluginsHash: "abc",
		})
		if err != nil {
			t.Fatal(err)
		}
		stream.Close() //nolint:errcheck
		env := strings.Join(rt.ranEnv, " ")
		if !strings.Contains(env, "INDIGO_TARGET="+PluginsPath("abc")+" INDIGO_LINK="+StablePluginsLink) {
			t.Errorf("no plugins link update in run env %q", env)
		}
	})
	t.Run("no plugins", func(t *testing.T) {
		rt := &fakeRuntime{arch: "arm64", exists: true}
		locate, _, _ := locateReal(t, "a server binary")
		stream, err := Attach(context.Background(), rt, "c1", "/w", AttachOptions{Locate: locate})
		if err != nil {
			t.Fatal(err)
		}
		stream.Close() //nolint:errcheck
		removed := false
		for _, argv := range rt.ranArgv {
			if strings.Contains(argv, "rm -f") {
				removed = true
			}
		}
		if !removed || !strings.Contains(strings.Join(rt.ranEnv, " "), "INDIGO_LINK="+StablePluginsLink) {
			t.Errorf("stale plugins link not removed; ran %q with env %q", rt.ranArgv, rt.ranEnv)
		}
	})
}
