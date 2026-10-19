package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/indiejames/indigo/sdk/pluginproto"
)

// countingKeyHandler counts the keys it is asked to handle.
type countingKeyHandler struct{ calls atomic.Int32 }

func (h *countingKeyHandler) Handle(_ context.Context, call pluginproto.KeyHandler_handle) error {
	h.calls.Add(1)
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	r, err := res.NewResponse()
	if err != nil {
		return err
	}
	r.SetHandled(true)
	return nil
}

// TestRegisterKeyBindingIsRefused: plugins do not bind keys. A plugin built
// against an older SDK that still calls registerKeyBinding is told so, and the
// key it asked for never reaches it. Before SDK 0.2.0 the binding was checked
// ahead of the editor's own commands, so a plugin could silently replace any
// Normal-mode key, and two plugins binding one key raced for it.
func TestRegisterKeyBindingIsRefused(t *testing.T) {
	reg := &registeredPlugin{
		name:        "old",
		insertHooks: map[string]pluginproto.KeyHandler{},
		commands:    map[string]pluginproto.CommandHandler{},
		menuActions: map[string]pluginproto.KeyHandler{},
	}
	api := pluginproto.EditorApi_ServerToClient(&editorApiServer{reg: reg})
	defer api.Release()
	handler := &countingKeyHandler{}
	h := pluginproto.KeyHandler_ServerToClient(handler)
	defer h.Release()

	fut, rel := api.RegisterKeyBinding(context.Background(), func(p pluginproto.EditorApi_registerKeyBinding_Params) error {
		if err := p.SetTrigger("d"); err != nil {
			return err
		}
		return p.SetHandler(h.AddRef())
	})
	_, err := fut.Struct()
	rel()
	if err == nil || !strings.Contains(err.Error(), "OnMenuAction") {
		t.Fatalf("registerKeyBinding returned %v, want a refusal pointing at OnMenuAction", err)
	}

	m := &Manager{plugins: []*registeredPlugin{reg}}
	if handled, _, _, _, _, _, err := m.HandleKey(context.Background(), "d", "normal", 1, 1, 0, 0); handled || err != nil {
		t.Errorf("d in normal mode: handled %v, err %v; it must stay the editor's", handled, err)
	}
	if n := handler.calls.Load(); n != 0 {
		t.Errorf("the plugin's handler ran %d times for a key it asked to bind", n)
	}
}

// TestInsertHooksStillReachPlugins keeps what removing key bindings must not
// take with it: a typed character with an insert hook still reaches its
// plugin through HandleKey.
func TestInsertHooksStillReachPlugins(t *testing.T) {
	handler := &countingKeyHandler{}
	h := pluginproto.KeyHandler_ServerToClient(handler)
	defer h.Release()
	reg := &registeredPlugin{name: "hooks", insertHooks: map[string]pluginproto.KeyHandler{".": h}}
	m := &Manager{plugins: []*registeredPlugin{reg}}

	handled, _, _, _, _, _, err := m.HandleKey(context.Background(), ".", "insert", 1, 1, 0, 0)
	if err != nil || !handled || handler.calls.Load() != 1 {
		t.Errorf("insert hook for '.': handled %v, err %v, calls %d; want it delivered once", handled, err, handler.calls.Load())
	}
}

// TestRetiredKeyFieldsAreReported: trigger_key and key_description used to
// bind a plugin's key. TOML ignores unknown fields, so a manifest that still
// has them must be reported, or the author sees the key stop working with
// nothing saying why.
func TestRetiredKeyFieldsAreReported(t *testing.T) {
	var manifest PluginToml
	md, err := toml.Decode("name = \"old\"\ntrigger_key = \"alt+m\"\nkey_description = \"x\"\ndescription = \"d\"\n", &manifest)
	if err != nil {
		t.Fatal(err)
	}
	got := retiredKeyFields(md)
	if len(got) != 2 || got[0] != "trigger_key" || got[1] != "key_description" {
		t.Errorf("retired fields = %q, want trigger_key and key_description", got)
	}
}

// TestBundledPluginsDeclareNoRetiredOrUnknownFields: the bundled manifests are
// what plugin authors copy, so they must carry none of the retired key fields,
// and no field the editor would ignore, which is how a typo hides.
func TestBundledPluginsDeclareNoRetiredOrUnknownFields(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "plugins", "*", "plugin.toml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("found no bundled manifests (%v)", err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var manifest PluginToml
		md, err := toml.Decode(string(data), &manifest)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if keys := md.Undecoded(); len(keys) > 0 {
			t.Errorf("%s: fields the editor ignores: %v", path, keys)
		}
	}
}
