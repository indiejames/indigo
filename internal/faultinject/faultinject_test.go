package faultinject

import "testing"

// TestFaultInjectionIsCompiledOutByDefault is the test that matters most in
// this package, and it asserts the *absence* of a feature.
//
// The whole safety argument is that a shipped binary cannot be made to corrupt
// a buffer on request, whatever its environment. An accidental inversion — a
// build tag typo, a file renamed, the two files drifting — would not fail
// anything else in the suite: it would quietly arm a release, and every other
// test would go on passing.
//
// Run under the default build this asserts the hooks are inert; run with
// -tags indigo_debug it asserts they work. So `make test` proves the release is
// safe and `make test-faults` proves the tool is real, and neither can be
// mistaken for the other.
func TestFaultInjectionIsCompiledOutByDefault(t *testing.T) {
	t.Cleanup(Reset)

	FailNextApplyOps(5)
	BumpGenerationOnNextApplies(5)
	DropNextPollResponses(5)

	if !Enabled() {
		if ShouldFailApplyOp() || ShouldBumpGeneration() || ShouldDropPoll() {
			t.Fatal("faults fired in a build without -tags indigo_debug: a release " +
				"binary can be made to corrupt a buffer")
		}
		if len(Armed()) != 0 {
			t.Errorf("Armed() = %v in the default build, want nothing", Armed())
		}
		return
	}

	// -tags indigo_debug: the same calls must now do something, or the switch
	// is decorative and the paths it exists to reach are still unreachable.
	if !ShouldFailApplyOp() {
		t.Error("armed fail_apply did not fire under -tags indigo_debug")
	}
	if !ShouldBumpGeneration() {
		t.Error("armed bump_generation did not fire under -tags indigo_debug")
	}
	if !ShouldDropPoll() {
		t.Error("armed drop_poll did not fire under -tags indigo_debug")
	}
}

// TestFaultsAreConsumedExactlyNTimes covers the counter, which is the point of
// arming a number rather than a flag: "the retry fails too" is what separates a
// resync that recovers from one that loops.
func TestFaultsAreConsumedExactlyNTimes(t *testing.T) {
	if !Enabled() {
		t.Skip("hooks compiled out; run with -tags indigo_debug")
	}
	t.Cleanup(Reset)

	FailNextApplyOps(2)
	// Each call consumes one, so they are counted rather than combined into one
	// expression — which reads as a typo and lints as one (SA4000).
	fired := 0
	for i := 0; i < 3; i++ {
		if ShouldFailApplyOp() {
			fired++
		}
	}
	if fired != 2 {
		t.Errorf("armed 2 failures, %d fired across 3 calls — faults must be "+
			"consumed exactly n times, neither latched on nor lost", fired)
	}
}

func TestResetDisarmsEverything(t *testing.T) {
	if !Enabled() {
		t.Skip("hooks compiled out; run with -tags indigo_debug")
	}
	FailNextApplyOps(3)
	DropNextPollResponses(3)
	Reset()
	if ShouldFailApplyOp() || ShouldDropPoll() {
		t.Error("Reset left a fault armed — one test could arm another")
	}
	if len(Armed()) != 0 {
		t.Errorf("Armed() = %v after Reset, want nothing", Armed())
	}
}
