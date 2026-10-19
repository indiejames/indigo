// hello is a simple test plugin that just prints "hello" in the status bar when
// the user picks it from the Command menu (space h).
package main

import (
	"sync"

	"github.com/indiejames/indigo/sdk"
)

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
	api.OnMenuAction("hello.say", h.onSay)                          //nolint:errcheck
	api.BroadcastMessage("hello ready: press space h to say hello") //nolint:errcheck
	return sdk.Info{Name: "hello", Version: "0.2.0"}
}

// onSay runs when the Hello item is picked from the Command menu.
func (h *Hello) onSay(_ string, ctx sdk.KeyContext) sdk.KeyResponse {
	return h.trigger(ctx)
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

func main() {
	if err := sdk.Run(&Hello{}); err != nil {
		panic(err)
	}
}
