package server

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestApplyOpsRejectsStaleExpectedText is the gap this closes. Coordinates are
// rebased correctly, so the edit lands in the right *place* — but the text there
// is no longer what the caller was describing, because it read the file before
// another client changed it. Without the check the edit succeeds and replaces
// the wrong thing, silently.
func TestApplyOpsRejectsStaleExpectedText(t *testing.T) {
	_, entry, cl := newOTTestService(t, "alpha beta gamma\n", 1, 2)

	// Client 2's view is from before client 1 renamed "beta" to "BETA".
	if err := sendOp(t, cl, 1, 0, document.Op{
		Type: document.OpDelete, FromLine: 0, FromCol: 6, ToLine: 0, ToCol: 10,
	}); err != nil {
		t.Fatalf("seeding delete: %v", err)
	}
	if err := sendOp(t, cl, 1, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 6, InsertText: "BETA",
	}); err != nil {
		t.Fatalf("seeding insert: %v", err)
	}
	before := entry.buf.Content()

	// Client 2 now tries to replace what it still believes is "beta".
	err := sendOp(t, cl, 2, 0, document.Op{
		Type: document.OpDelete, FromLine: 0, FromCol: 6, ToLine: 0, ToCol: 10,
		ExpectText: "beta",
	})
	if err == nil {
		t.Fatalf("a stale expectation was accepted; the buffer became %q", entry.buf.Content())
	}
	// Rebasing cancels the delete outright here, because the text it targeted
	// was already removed — so the failure to report is "it is gone", not "it
	// changed". Reporting success for an edit that did nothing is the bug.
	if !strings.Contains(err.Error(), "no longer present") {
		t.Errorf("error = %q, want it to say the text is gone", err)
	}
	if !strings.Contains(err.Error(), "beta") {
		t.Errorf("error = %q, want it to name the text that vanished", err)
	}
	if got := entry.buf.Content(); got != before {
		t.Errorf("buffer changed to %q despite the rejection", got)
	}
}

// TestApplyOpsAcceptsMatchingExpectedText is the positive control: the guard
// must not refuse ordinary edits.
func TestApplyOpsAcceptsMatchingExpectedText(t *testing.T) {
	_, entry, cl := newOTTestService(t, "alpha beta gamma\n", 1)

	if err := sendOp(t, cl, 1, 0, document.Op{
		Type: document.OpDelete, FromLine: 0, FromCol: 6, ToLine: 0, ToCol: 10,
		ExpectText: "beta",
	}); err != nil {
		t.Fatalf("a matching expectation was rejected: %v", err)
	}
	if got := entry.buf.Content(); got != "alpha  gamma\n" {
		t.Errorf("content = %q, want %q", got, "alpha  gamma\n")
	}
}

// TestExpectedTextIsCheckedAfterRebasing is the half that makes this usable at
// all. A concurrent edit *elsewhere* moves the target text without changing it,
// and the caller's coordinates are stale by exactly that shift. Checking the
// original range would report a spurious mismatch and refuse a perfectly valid
// edit; checking the rebased range accepts it.
func TestExpectedTextIsCheckedAfterRebasing(t *testing.T) {
	_, entry, cl := newOTTestService(t, "alpha beta gamma\n", 1, 2)

	// Client 1 inserts ahead of the target, shifting it right by four.
	if err := sendOp(t, cl, 1, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "XXXX",
	}); err != nil {
		t.Fatalf("seeding insert: %v", err)
	}

	// Client 2's coordinates predate that, and are rebased past it.
	if err := sendOp(t, cl, 2, 0, document.Op{
		Type: document.OpDelete, FromLine: 0, FromCol: 6, ToLine: 0, ToCol: 10,
		ExpectText: "beta",
	}); err != nil {
		t.Fatalf("expectation checked against un-rebased coordinates: %v", err)
	}
	if got := entry.buf.Content(); got != "XXXXalpha  gamma\n" {
		t.Errorf("content = %q, want %q", got, "XXXXalpha  gamma\n")
	}
}

// TestExpectedTextRejectsPartialOverlap covers the third outcome: another
// client edited *inside* the target, so rebasing splits the delete around their
// change. Applying it would remove part of what the caller described and leave
// the rest — a half-done replace, reported as success.
func TestExpectedTextRejectsPartialOverlap(t *testing.T) {
	_, entry, cl := newOTTestService(t, "alpha beta gamma\n", 1, 2)

	// Client 1 inserts inside "beta", making it "beXXta".
	if err := sendOp(t, cl, 1, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 8, InsertText: "XX",
	}); err != nil {
		t.Fatalf("seeding insert: %v", err)
	}
	before := entry.buf.Content()

	err := sendOp(t, cl, 2, 0, document.Op{
		Type: document.OpDelete, FromLine: 0, FromCol: 6, ToLine: 0, ToCol: 10,
		ExpectText: "beta",
	})
	if err == nil {
		t.Fatalf("a split expectation was accepted; buffer became %q", entry.buf.Content())
	}
	if !strings.Contains(err.Error(), "only part of it") {
		t.Errorf("error = %q, want it to say the edit would apply to only part of the text", err)
	}
	if got := entry.buf.Content(); got != before {
		t.Errorf("buffer changed to %q despite the rejection", got)
	}
}

// TestExpectedTextIgnoredWhenAbsent: ops without an expectation are unaffected,
// so every existing caller keeps working.
func TestExpectedTextIgnoredWhenAbsent(t *testing.T) {
	_, entry, cl := newOTTestService(t, "alpha beta gamma\n", 1)

	if err := sendOp(t, cl, 1, 0, document.Op{
		Type: document.OpDelete, FromLine: 0, FromCol: 6, ToLine: 0, ToCol: 10,
	}); err != nil {
		t.Fatalf("an op without an expectation was rejected: %v", err)
	}
	if got := entry.buf.Content(); got != "alpha  gamma\n" {
		t.Errorf("content = %q, want %q", got, "alpha  gamma\n")
	}
}

// TestVerifyExpectedTextAllowsRepeatedExpectations covers a batch carrying the
// same expected text twice — two occurrences of one string being replaced
// together.
//
// Transform carries ExpectText onto both halves when a delete splits, which is
// what makes "more than one survivor" mean "someone edited inside it". Matching
// survivors by text alone then counts the *other* expectation's delete as a
// split of this one, and refuses a batch in which nothing is wrong.
//
// No caller can produce this today — every batch that sets ExpectText carries
// exactly one delete — so this guards the invariant rather than a live path.
func TestVerifyExpectedTextAllowsRepeatedExpectations(t *testing.T) {
	buf := document.New("a.go", "foo bar foo\n")

	// Two deletes of "foo", at columns 0 and 8. Nothing concurrent happened, so
	// rebasing left them exactly as sent.
	ops := []document.Op{
		{Type: document.OpDelete, FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 3, ExpectText: "foo"},
		{Type: document.OpDelete, FromLine: 0, FromCol: 8, ToLine: 0, ToCol: 11, ExpectText: "foo"},
	}

	if err := verifyExpectedText(buf, ops, ops); err != nil {
		t.Fatalf("a batch replacing two occurrences of one string was refused: %v", err)
	}
}

// TestVerifyExpectedTextStillDetectsASplitAmongRepeats is the complement: with
// two expectations of one text, three survivors still means one of them was
// split by a concurrent edit, and the batch must still be refused.
func TestVerifyExpectedTextStillDetectsASplitAmongRepeats(t *testing.T) {
	buf := document.New("a.go", "foo bar foo\n")
	input := []document.Op{
		{Type: document.OpDelete, FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 3, ExpectText: "foo"},
		{Type: document.OpDelete, FromLine: 0, FromCol: 8, ToLine: 0, ToCol: 11, ExpectText: "foo"},
	}
	rebased := append(append([]document.Op{}, input...), document.Op{
		Type: document.OpDelete, FromLine: 0, FromCol: 4, ToLine: 0, ToCol: 5, ExpectText: "foo",
	})

	err := verifyExpectedText(buf, input, rebased)
	if err == nil {
		t.Fatal("a split among repeated expectations was accepted")
	}
	if !strings.Contains(err.Error(), "only part of it") {
		t.Errorf("error = %q, want it to report a partial application", err)
	}
}

// TestVerifyExpectedTextDetectsOneOfSeveralRemoved covers the other direction:
// two expectations, one survivor. One of the two occurrences is gone, so the
// batch would apply to only half of what it described.
func TestVerifyExpectedTextDetectsOneOfSeveralRemoved(t *testing.T) {
	buf := document.New("a.go", "foo bar foo\n")
	input := []document.Op{
		{Type: document.OpDelete, FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 3, ExpectText: "foo"},
		{Type: document.OpDelete, FromLine: 0, FromCol: 8, ToLine: 0, ToCol: 11, ExpectText: "foo"},
	}
	rebased := input[:1]

	err := verifyExpectedText(buf, input, rebased)
	if err == nil {
		t.Fatal("a batch missing one of its two expectations was accepted")
	}
	if !strings.Contains(err.Error(), "every place") {
		t.Errorf("error = %q, want it to say not every occurrence survived", err)
	}
}
