package app

import (
	"reflect"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/hangdetect"
)

// UILoopTag names the Bubble Tea update loop to the hang detector. It doubles
// as the debuglog tag its reports are written under, so a frozen window's
// evidence sorts in with the rest of that window's log rather than into a
// stream of its own.
const UILoopTag = "app"

// beat is the seam the wiring test swaps. A handler and the dispatch that
// reaches it need separate tests — each passes without the other, and a
// heartbeat nothing calls reports silence, which looks exactly like health.
var beat = hangdetect.Beat

// msgName describes a message for a stall report: the concrete type, and
// nothing else.
//
// Deliberately not the message's contents. The one message whose contents look
// most useful here is a key press, and that is a character the user typed —
// this output lands in a shared temp directory and gets pasted into bug
// reports, and "a key press froze it" is the distinction that matters anyway,
// not which key. Same line this codebase already draws for describeOp, which
// reports an insert's position and size and never its text.
func msgName(msg tea.Msg) string {
	if msg == nil {
		return "nil"
	}
	t := reflect.TypeOf(msg)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if pkg := t.PkgPath(); pkg != "" {
		return shortPkg(pkg) + "." + t.Name()
	}
	return t.String()
}

// shortPkg keeps the last element of an import path, so a report says
// "app.bufferOpenedMsg" rather than repeating the module path on every line.
func shortPkg(pkg string) string {
	for i := len(pkg) - 1; i >= 0; i-- {
		if pkg[i] == '/' {
			return pkg[i+1:]
		}
	}
	return pkg
}
