// Command miniplugin is a minimal real plugin binary used only by
// internal/plugin's integration test (manager_integration_test.go) to
// exercise Manager.Start's real spawn → RPC handshake → dispatch path
// against an actual child process, rather than a hand-rolled fake.
package main

import (
	"os"

	"github.com/indiejames/indigo/sdk"
)

type miniPlugin struct{}

func (miniPlugin) Init(api *sdk.Api) sdk.Info {
	api.OnKey("X", func(key string, ctx sdk.KeyContext) sdk.KeyResponse { //nolint:errcheck
		return sdk.KeyResponse{
			Handled: true,
			Edits: []sdk.TextEdit{{
				From:    sdk.Position{Line: 0, Col: 0},
				To:      sdk.Position{Line: 0, Col: 0},
				NewText: "miniplugin-was-here",
			}},
		}
	})
	api.OnWorkspaceScan(func() { //nolint:errcheck
		// Marker-file signal: this process is a separate spawned binary, so
		// the test can't observe an in-memory flag directly. The marker
		// path travels in via env var (os.StartProcess propagates the
		// parent's environment — see manager.go's startPlugin), set by the
		// test with t.Setenv before Manager.Start spawns this process.
		if marker := os.Getenv("MINIPLUGIN_SCAN_MARKER"); marker != "" {
			os.WriteFile(marker, []byte("scanned"), 0o644) //nolint:errcheck
		}
	})

	api.CompletionsFull(sdk.CompletionHandlers{ //nolint:errcheck
		GetCompletions: func(bufID, line, col uint32) []sdk.CompletionItem {
			return []sdk.CompletionItem{{
				Label:      "mini-item",
				InsertText: "mini-item",
				Data:       "resolve-me",
			}}
		},
		ResolveCompletion: func(item sdk.CompletionItem) sdk.CompletionItem {
			item.Detail = "resolved:" + item.Data
			return item
		},
	})
	return sdk.Info{Name: "miniplugin", Version: "0.0.1"}
}

func main() {
	if err := sdk.Run(miniPlugin{}); err != nil {
		panic(err)
	}
}
