// Bookmarks plugin for indigo.
//
// alt+m in normal mode: toggle a bookmark on the current line (prompts for name).
// alt+b in normal mode: open the bookmark picker popup.
// In the popup: Enter to jump, r to rename, d to delete, Esc to close.
package main

import (
	"fmt"
	"os"

	"github.com/indiejames/indigo/sdk"
)

type Bookmarks struct {
	api *sdk.Api
}

func (b *Bookmarks) Init(api *sdk.Api) sdk.Info {
	b.api = api

	api.OnKey("alt+m", b.onAltM) //nolint:errcheck
	api.OnKey("alt+b", b.onAltB) //nolint:errcheck

	return sdk.Info{Name: "bookmarks", Version: "0.1.0"}
}

func (b *Bookmarks) onAltM(key string, ctx sdk.KeyContext) sdk.KeyResponse {
	if ctx.Mode != "normal" {
		return sdk.KeyResponse{Handled: false}
	}
	b.api.PromptBookmarkName(ctx.BufID, ctx.CursorLine, ctx.CursorCol, "▶") //nolint:errcheck
	return sdk.KeyResponse{Handled: true}
}

func (b *Bookmarks) onAltB(key string, ctx sdk.KeyContext) sdk.KeyResponse {
	if ctx.Mode != "normal" {
		return sdk.KeyResponse{Handled: false}
	}
	b.api.ShowBookmarks() //nolint:errcheck
	return sdk.KeyResponse{Handled: true}
}

func main() {
	if err := sdk.Run(&Bookmarks{}); err != nil {
		fmt.Fprintf(os.Stderr, "bookmarks: %v\n", err)
		os.Exit(1)
	}
}
