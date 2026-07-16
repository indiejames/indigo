// hello is a simple test plugin that just prints "hello" in the status bar when
// the user presses H.
package main

import (
	"sync"

	"github.com/indiejames/indigo/sdk"
)

// labelChars defines the character set used for 2-char labels.
// Home-row characters first for ergonomics.
const labelChars = "asdfjkl;ghqwertyuiopzxcvbnm"

type target struct {
	label string
	line  uint32
	col   uint32
}

// Hello holds all state for one running plugin instance.
type Hello struct {
	mu  sync.Mutex
	api *sdk.Api

	// jump session state (protected by mu)
	active   bool
	bufID    uint32
	clientID uint64
}

func (h *Hello) Init(api *sdk.Api) sdk.Info {
	h.api = api
	api.OnKey("H", h.onKey)        //nolint:errcheck
	api.BroadcastMessage("hello ready: press H to say hello") //nolint:errcheck
	return sdk.Info{Name: "hello", Version: "0.1.0"}
}

// onKey is the single handler registered for "H". It dispatches on ctx.Mode:
//   - "normal"  → trigger: print hello in status bar
func (h *Hello) onKey(key string, ctx sdk.KeyContext) sdk.KeyResponse {
	switch ctx.Mode {
	case "normal":
		return h.trigger(ctx)
	default:
		return sdk.KeyResponse{}
	}
}

func (h *Hello) trigger(ctx sdk.KeyContext) sdk.KeyResponse {
	h.mu.Lock()
	h.active = true
	h.bufID = ctx.BufID
	h.clientID = ctx.ClientID
	h.mu.Unlock()

	// h.api.ShowMessageTo(ctx.ClientID, "hello: activated") //nolint:errcheck
	h.api.BroadcastMessage("hello: activated") //nolint:errcheck

	return sdk.KeyResponse{Handled: true}
}


func (j *Hello) reset() {
	j.active = false
}

func main() {
	if err := sdk.Run(&Hello{}); err != nil {
		panic(err)
	}
}
