package app

import (
	"github.com/indiejames/indigo/internal/workspacefs"
)

// GrepResult is one match found during workspace search.
//
// All that is left of the client's own view of the workspace filesystem: a type
// the grep picker renders. Listing, search, the recent-files filter and the
// new-file directory checks all go through the server now — see
// internal/client/rpc_workspacefs.go.
//
// The ignore set went with them. It is config-driven, the server loads the
// config that describes the filesystem it can see, and nothing on this side
// reads the set any more.
type GrepResult = workspacefs.Result
