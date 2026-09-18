//go:build !indigo_debug

// Package faultinject makes the failure paths reachable on purpose.
//
// Rejected ops, generation mismatches, resyncs and dropped poll responses are
// the paths that keep shipping broken, and the reason is that reaching them
// requires a race to go a particular way. Every one of them has been found by
// reading code or by a user hitting it, never by a test that simply asked for
// it. This asks for it.
//
// # Gating
//
// This file is the build without the hooks, and it is the default. Every
// setter is a no-op and every predicate is a constant false, so the compiler
// removes the call and the branch behind it: a shipped binary does not contain
// the ability to lie to itself, whatever its environment says. That is a
// stronger guarantee than an environment variable, which a production binary
// can still be talked into honouring — and "make the editor corrupt a buffer on
// request" is not a switch to leave within reach of a misconfiguration.
//
// The hooks live in faultinject_on.go behind `-tags indigo_debug`. Build or
// test with that tag to arm them:
//
//	go test -tags "lang_all indigo_debug" ./internal/server/
//
// Both files must export the same API, or the tagged build stops compiling.
// TestFaultInjectionIsCompiledOutByDefault guards the direction that matters —
// that this file really is inert — because an accidental inversion here would
// not fail anything, it would quietly arm a release.
package faultinject

// Enabled reports whether the hooks are compiled in. Call sites do not need it;
// it exists so a test can say which build it is running under, and so a
// diagnostic can report the truth rather than assuming.
func Enabled() bool { return false }

// FailNextApplyOps makes the next n ApplyOp/ApplyOps calls fail.
func FailNextApplyOps(int) {}

// ShouldFailApplyOp reports whether this call should fail, consuming one of the
// armed failures.
func ShouldFailApplyOp() bool { return false }

// BumpGenerationOnNextApplies makes the server's next n applies behave as
// though the buffer had been swapped wholesale underneath the client.
func BumpGenerationOnNextApplies(int) {}

// ShouldBumpGeneration consumes one armed generation bump.
func ShouldBumpGeneration() bool { return false }

// DropNextPollResponses makes the next n GetUpdates responses come back empty,
// as though they were lost in flight.
func DropNextPollResponses(int) {}

// ShouldDropPoll consumes one armed dropped poll.
func ShouldDropPoll() bool { return false }

// Reset disarms everything. Tests defer it so one case cannot arm another.
func Reset() {}

// Armed returns what is still armed, for a diagnostic or a test failure
// message. Always empty here.
func Armed() map[string]int { return nil }
