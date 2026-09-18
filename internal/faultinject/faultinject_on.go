//go:build indigo_debug

package faultinject

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// See faultinject_off.go for what this package is for and why it is gated by a
// build tag rather than an environment variable.
//
// Counters rather than booleans: "fail the next 3" is the interesting request.
// A one-shot cannot express "the retry fails too", which is the case that
// separates a resync that recovers from one that loops.
//
// Guarded by a mutex because the server applies ops on the capnp call queue
// while a test arms faults from its own goroutine, and the whole point of this
// package is to be used under -race.
var (
	mu             sync.Mutex
	failApplyOps   int
	bumpGeneration int
	dropPolls      int
)

func init() {
	// Arming from the environment, inside the tagged build only, so a debug
	// binary can be launched with faults already set for a hands-on session:
	//
	//	INDIGO_FAULTS=fail_apply:3,drop_poll:1,bump_generation:1 ./indigo file.go
	//
	// Unparseable entries are ignored rather than fatal: this is a debugging
	// aid, and refusing to start because a fault name was misspelled would be
	// the wrong trade when someone is mid-investigation.
	for _, part := range strings.Split(os.Getenv("INDIGO_FAULTS"), ",") {
		name, count, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(count)
		if err != nil || n <= 0 {
			continue
		}
		switch name {
		case "fail_apply":
			failApplyOps = n
		case "bump_generation":
			bumpGeneration = n
		case "drop_poll":
			dropPolls = n
		}
	}
}

func Enabled() bool { return true }

func FailNextApplyOps(n int) {
	mu.Lock()
	defer mu.Unlock()
	failApplyOps = n
}

func ShouldFailApplyOp() bool {
	mu.Lock()
	defer mu.Unlock()
	if failApplyOps <= 0 {
		return false
	}
	failApplyOps--
	return true
}

func BumpGenerationOnNextApplies(n int) {
	mu.Lock()
	defer mu.Unlock()
	bumpGeneration = n
}

func ShouldBumpGeneration() bool {
	mu.Lock()
	defer mu.Unlock()
	if bumpGeneration <= 0 {
		return false
	}
	bumpGeneration--
	return true
}

func DropNextPollResponses(n int) {
	mu.Lock()
	defer mu.Unlock()
	dropPolls = n
}

func ShouldDropPoll() bool {
	mu.Lock()
	defer mu.Unlock()
	if dropPolls <= 0 {
		return false
	}
	dropPolls--
	return true
}

func Reset() {
	mu.Lock()
	defer mu.Unlock()
	failApplyOps, bumpGeneration, dropPolls = 0, 0, 0
}

func Armed() map[string]int {
	mu.Lock()
	defer mu.Unlock()
	out := map[string]int{}
	if failApplyOps > 0 {
		out["fail_apply"] = failApplyOps
	}
	if bumpGeneration > 0 {
		out["bump_generation"] = bumpGeneration
	}
	if dropPolls > 0 {
		out["drop_poll"] = dropPolls
	}
	return out
}
