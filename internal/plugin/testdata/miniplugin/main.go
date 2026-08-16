// Command miniplugin is a minimal real plugin binary used only by
// internal/plugin's integration test (manager_integration_test.go) to
// exercise Manager.Start's real spawn → RPC handshake → dispatch path
// against an actual child process, rather than a hand-rolled fake.
package main

import (
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
	return sdk.Info{Name: "miniplugin", Version: "0.0.1"}
}

func main() {
	if err := sdk.Run(miniPlugin{}); err != nil {
		panic(err)
	}
}
