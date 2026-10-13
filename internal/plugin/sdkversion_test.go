package plugin

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/sdk"
)

func TestCheckSDKVersion(t *testing.T) {
	tests := []struct {
		name      string
		declared  string
		supported string
		wantErr   string // substring; "" means compatible
		wantWarn  bool
	}{
		{name: "exact match", declared: "0.1.0", supported: "0.1.0"},
		{name: "patch differs either way in 0.x", declared: "0.1.7", supported: "0.1.2"},
		{name: "v prefix and prerelease ignored", declared: "v0.1.0-rc.1", supported: "0.1.0"},
		{name: "missing is accepted with a warning", declared: "", supported: "0.1.0", wantWarn: true},
		{name: "whitespace-only counts as missing", declared: "  ", supported: "0.1.0", wantWarn: true},

		{name: "0.x older minor refused", declared: "0.1.0", supported: "0.2.0", wantErr: "rebuild the plugin against SDK 0.2"},
		{name: "0.x newer minor refused", declared: "0.3.0", supported: "0.2.0", wantErr: "upgrade indigo"},

		{name: "1.x older minor accepted", declared: "1.2.0", supported: "1.5.3"},
		{name: "1.x newer minor refused", declared: "1.6.0", supported: "1.5.3", wantErr: "upgrade indigo"},
		{name: "1.x newer patch accepted", declared: "1.5.9", supported: "1.5.3"},
		{name: "older major refused", declared: "1.4.0", supported: "2.0.0", wantErr: "rebuild the plugin against SDK 2.0"},
		{name: "newer major refused", declared: "2.0.0", supported: "1.9.0", wantErr: "upgrade indigo"},
		{name: "0 vs 1 major refused", declared: "0.9.0", supported: "1.0.0", wantErr: "major versions differ"},

		{name: "two components refused", declared: "0.1", supported: "0.1.0", wantErr: "invalid sdk_version"},
		{name: "non-numeric refused", declared: "latest", supported: "0.1.0", wantErr: "invalid sdk_version"},
		{name: "negative refused", declared: "0.-1.0", supported: "0.1.0", wantErr: "invalid sdk_version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warn, err := checkSDKVersion(tt.declared, tt.supported)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkSDKVersion(%q, %q) = %v, want compatible", tt.declared, tt.supported, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("checkSDKVersion(%q, %q) error = %v, want one containing %q", tt.declared, tt.supported, err, tt.wantErr)
			}
			if (warn != "") != tt.wantWarn {
				t.Errorf("warning = %q, want warning: %v", warn, tt.wantWarn)
			}
		})
	}
}

// The editor's own version must parse, or every plugin that declares one is
// refused. sdk.Version is hand-maintained alongside the sdk/vX.Y.Z tag.
func TestEditorSDKVersionIsValid(t *testing.T) {
	if _, err := parseSDKVersion(sdk.Version); err != nil {
		t.Fatalf("sdk.Version: %v", err)
	}
	if _, err := checkSDKVersion(sdk.Version, sdk.Version); err != nil {
		t.Fatalf("sdk.Version is not compatible with itself: %v", err)
	}
}

// TestStartRefusesIncompatibleSDKVersion covers the wiring rather than the
// rule: the same real, otherwise-startable miniplugin is started when its
// manifest declares this editor's SDK version and refused when it declares an
// incompatible one. The compatible half is what makes the refusal meaningful
// — a plugin that failed to start for any other reason would also leave
// m.plugins empty.
func TestStartRefusesIncompatibleSDKVersion(t *testing.T) {
	for _, tc := range []struct {
		declared  string
		wantStart bool
	}{
		{declared: sdk.Version, wantStart: true},
		{declared: "99.0.0", wantStart: false},
	} {
		t.Run(tc.declared, func(t *testing.T) {
			installMiniPlugin(t, "sdk_version = \""+tc.declared+"\"\n")

			m := NewManager(t.TempDir(), nil)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := m.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(m.Shutdown)

			m.mu.Lock()
			n := len(m.plugins)
			m.mu.Unlock()
			if started := n == 1; started != tc.wantStart {
				t.Fatalf("sdk_version %q: %d plugin(s) started, want started=%v", tc.declared, n, tc.wantStart)
			}
		})
	}
}

// Bumping sdk.Version without bumping the bundled plugins' manifests would
// ship an editor that refuses its own plugins, so every plugin in this
// repository must declare a version this editor accepts — and declare one at
// all, since they are the examples third-party authors copy.
func TestBundledPluginsDeclareACompatibleSDKVersion(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "..", "plugins", "*", "plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("found no bundled plugin manifests; has plugins/ moved?")
	}
	for _, path := range dirs {
		m, err := loadManifest(filepath.Dir(path))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if m.SdkVersion == "" {
			t.Errorf("%s: no sdk_version", path)
			continue
		}
		if _, err := checkSDKVersion(m.SdkVersion, sdk.Version); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}
