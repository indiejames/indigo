package main

import (
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const samplePackageJSON = `{
  "name": "demo",
  "scripts": {
    "build": "tsc",
    "test": "jest"
  },
  "dependencies": {
    "lodash": "^4.17.21",
    "react": ""
  },
  "devDependencies": {
    "eslint": "^8.0.0"
  }
}
`

func TestDepValueAtFindsDependencyValue(t *testing.T) {
	lines := strings.Split(samplePackageJSON, "\n")
	// line 7 (0-based) is `    "lodash": "^4.17.21",`
	line := 7
	if got := lines[line]; got != `    "lodash": "^4.17.21",` {
		t.Fatalf("test fixture drifted: line %d = %q", line, got)
	}
	col := len(`    "lodash": "^`) // cursor inside the value, after '^'

	dep, valStart, valEnd, ok := depValueAt(samplePackageJSON, line, col)
	if !ok {
		t.Fatal("depValueAt: ok = false, want true")
	}
	if dep != "lodash" {
		t.Errorf("dep = %q, want %q", dep, "lodash")
	}
	// The leading '^' is preserved (not part of the replacement range) —
	// see TestDepValueAtPreservesLeadingCaretOperator.
	wantStart := len(`    "lodash": "^`)
	wantEnd := len(`    "lodash": "`) + len("^4.17.21")
	if valStart != wantStart || valEnd != wantEnd {
		t.Errorf("valStart/valEnd = %d/%d, want %d/%d", valStart, valEnd, wantStart, wantEnd)
	}
}

func TestDepValueAtFindsDevDependencyValue(t *testing.T) {
	lines := strings.Split(samplePackageJSON, "\n")
	line := 11 // `    "eslint": "^8.0.0"`
	if got := lines[line]; got != `    "eslint": "^8.0.0"` {
		t.Fatalf("test fixture drifted: line %d = %q", line, got)
	}
	dep, _, _, ok := depValueAt(samplePackageJSON, line, 20)
	if !ok || dep != "eslint" {
		t.Errorf("depValueAt = (%q, ok=%v), want (\"eslint\", true)", dep, ok)
	}
}

func TestDepValueAtRejectsScriptsSection(t *testing.T) {
	lines := strings.Split(samplePackageJSON, "\n")
	line := 3 // `    "build": "tsc",` inside "scripts", not a dependency section
	if got := lines[line]; got != `    "build": "tsc",` {
		t.Fatalf("test fixture drifted: line %d = %q", line, got)
	}
	_, _, _, ok := depValueAt(samplePackageJSON, line, 15)
	if ok {
		t.Error("depValueAt: ok = true for a scripts entry, want false")
	}
}

func TestDepValueAtRejectsColOutsideValue(t *testing.T) {
	// Cursor on the key ("lodash"), not the value.
	line := 7
	_, _, _, ok := depValueAt(samplePackageJSON, line, 2)
	if ok {
		t.Error("depValueAt: ok = true with cursor on the key, want false")
	}
}

// TestDepValueAtPreservesLeadingCaretOperator is a regression test: a
// completion must never overwrite a semver range operator the user already
// typed. `"lodash": "^4.1"` with the cursor at the end must report a value
// range starting right after the '^', not including it, so accepting a
// completion inserts the version after the operator instead of replacing it.
func TestDepValueAtPreservesLeadingCaretOperator(t *testing.T) {
	content := "{\n  \"dependencies\": {\n    \"lodash\": \"^4.1\"\n  }\n}\n"
	line := 2 // `    "lodash": "^4.1"`
	rawLine := strings.Split(content, "\n")[line]
	if rawLine != `    "lodash": "^4.1"` {
		t.Fatalf("test fixture drifted: line %d = %q", line, rawLine)
	}
	col := len(rawLine) - 1 // cursor at end of value, right after "^4.1"

	dep, valStart, valEnd, ok := depValueAt(content, line, col)
	if !ok || dep != "lodash" {
		t.Fatalf("depValueAt = (%q, ok=%v), want (\"lodash\", true)", dep, ok)
	}
	caretCol := len(`    "lodash": "`)
	if valStart != caretCol+1 {
		t.Errorf("valStart = %d, want %d (right after the '^')", valStart, caretCol+1)
	}
	if valEnd != len(rawLine)-1 {
		t.Errorf("valEnd = %d, want %d", valEnd, len(rawLine)-1)
	}
}

// TestDepValueAtPreservesLeadingTildeOperator mirrors the caret test for '~'.
func TestDepValueAtPreservesLeadingTildeOperator(t *testing.T) {
	content := "{\n  \"dependencies\": {\n    \"lodash\": \"~4\"\n  }\n}\n"
	line := 2
	rawLine := strings.Split(content, "\n")[line]
	if rawLine != `    "lodash": "~4"` {
		t.Fatalf("test fixture drifted: line %d = %q", line, rawLine)
	}
	col := len(rawLine) - 1

	dep, valStart, _, ok := depValueAt(content, line, col)
	if !ok || dep != "lodash" {
		t.Fatalf("depValueAt = (%q, ok=%v), want (\"lodash\", true)", dep, ok)
	}
	tildeCol := len(`    "lodash": "`)
	if valStart != tildeCol+1 {
		t.Errorf("valStart = %d, want %d (right after the '~')", valStart, tildeCol+1)
	}
}

// TestDepValueAtOperatorOnlyValueYieldsEmptyRangeAfterIt covers the case the
// bug report actually hit: the user has typed only "^" so far (nothing after
// it yet) and triggers completion right there — the replacement range must
// be the empty span immediately after the operator, not the operator itself.
func TestDepValueAtOperatorOnlyValueYieldsEmptyRangeAfterIt(t *testing.T) {
	content := "{\n  \"dependencies\": {\n    \"lodash\": \"^\"\n  }\n}\n"
	line := 2
	rawLine := strings.Split(content, "\n")[line]
	if rawLine != `    "lodash": "^"` {
		t.Fatalf("test fixture drifted: line %d = %q", line, rawLine)
	}
	col := len(`    "lodash": "^`)

	dep, valStart, valEnd, ok := depValueAt(content, line, col)
	if !ok || dep != "lodash" {
		t.Fatalf("depValueAt = (%q, ok=%v), want (\"lodash\", true)", dep, ok)
	}
	if valStart != valEnd || valStart != col {
		t.Errorf("valStart/valEnd = %d/%d, want both == %d (empty range right after '^')", valStart, valEnd, col)
	}
}

func TestDepValueAtRejectsOutOfRangeLine(t *testing.T) {
	if _, _, _, ok := depValueAt(samplePackageJSON, -1, 0); ok {
		t.Error("depValueAt: ok = true for negative line, want false")
	}
	if _, _, _, ok := depValueAt(samplePackageJSON, 9999, 0); ok {
		t.Error("depValueAt: ok = true for out-of-range line, want false")
	}
}

func TestDepValueAtEmptyValueStillMatches(t *testing.T) {
	// `    "react": ""` — cursor right between the quotes of an empty value.
	lines := strings.Split(samplePackageJSON, "\n")
	line := 8
	if got := lines[line]; got != `    "react": ""` {
		t.Fatalf("test fixture drifted: line %d = %q", line, got)
	}
	col := len(`    "react": "`)
	dep, valStart, valEnd, ok := depValueAt(samplePackageJSON, line, col)
	if !ok || dep != "react" || valStart != valEnd {
		t.Errorf("depValueAt = (%q, %d, %d, %v), want (\"react\", equal, equal, true)", dep, valStart, valEnd, ok)
	}
}

func TestParseRegistryResponseOrdersNewestFirstAndFlagsLatest(t *testing.T) {
	body := []byte(`{
		"versions": {
			"1.0.0": {},
			"1.1.0": {},
			"2.0.0": {}
		},
		"time": {
			"1.0.0": "2020-01-01T00:00:00.000Z",
			"1.1.0": "2021-01-01T00:00:00.000Z",
			"2.0.0": "2022-01-01T00:00:00.000Z"
		},
		"dist-tags": {
			"latest": "2.0.0"
		}
	}`)

	versions, err := parseRegistryResponse(body)
	if err != nil {
		t.Fatalf("parseRegistryResponse: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("len(versions) = %d, want 3", len(versions))
	}
	want := []string{"2.0.0", "1.1.0", "1.0.0"}
	for i, w := range want {
		if versions[i].version != w {
			t.Errorf("versions[%d] = %q, want %q", i, versions[i].version, w)
		}
	}
	if !versions[0].latest {
		t.Error("versions[0] (2.0.0) should be flagged latest")
	}
	if versions[1].latest || versions[2].latest {
		t.Error("only the dist-tags latest version should be flagged")
	}
}

func TestParseRegistryResponseCapsAtMaxVersions(t *testing.T) {
	body := []byte(`{"versions": {`)
	for i := 0; i < maxVersions+25; i++ {
		if i > 0 {
			body = append(body, ',')
		}
		body = append(body, []byte(`"0.0.`+strconv.Itoa(i)+`": {}`)...)
	}
	body = append(body, []byte(`}}`)...)

	versions, err := parseRegistryResponse(body)
	if err != nil {
		t.Fatalf("parseRegistryResponse: %v", err)
	}
	if len(versions) != maxVersions {
		t.Errorf("len(versions) = %d, want %d (capped)", len(versions), maxVersions)
	}
}

func TestParseRegistryResponseMalformedJSONErrors(t *testing.T) {
	if _, err := parseRegistryResponse([]byte(`not json`)); err == nil {
		t.Error("parseRegistryResponse: err = nil for malformed JSON, want an error")
	}
}

// TestVersionsForReturnsDataOnFirstCallWhenFetchIsFast is the regression test
// for the reported bug: with no bounded wait, versionsFor's first call for
// any package always returned nil (the fetch it kicks off can't land before
// the call itself returns), forcing the user to keep retyping until some
// later keystroke happened to hit an already-warm cache. A fast fetch (the
// common case) must now be reflected in the very first call.
func TestVersionsForReturnsDataOnFirstCallWhenFetchIsFast(t *testing.T) {
	origFetch := fetchVersions
	t.Cleanup(func() { fetchVersions = origFetch })

	var calls int32
	fetchVersions = func(dep string) ([]versionInfo, error) {
		atomic.AddInt32(&calls, 1)
		return []versionInfo{{version: "1.0.0", latest: true}}, nil
	}

	p := &npmVersionsPlugin{cache: make(map[string]cacheEntry), inflight: make(map[string]chan struct{})}

	got := p.versionsFor("leftpad")
	if len(got) != 1 || got[0].version != "1.0.0" {
		t.Fatalf("first call = %v, want [{1.0.0 true}] (fast fetch should land within the bounded wait)", got)
	}

	// A second call must be served from the now-warm cache, not refetch.
	got2 := p.versionsFor("leftpad")
	if len(got2) != 1 || got2[0].version != "1.0.0" {
		t.Errorf("second call = %v, want [{1.0.0 true}]", got2)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("fetchVersions called %d times, want 1 (second call should hit the warm cache)", n)
	}
}

// TestVersionsForFallsBackToEmptyWhenFetchOutlivesWait verifies the graceful
// degradation path: a fetch slower than inflightWait doesn't block
// versionsFor indefinitely — it returns whatever's cached (nothing, for a
// package never seen before), and a later call after the fetch actually
// finishes picks up the now-warm cache.
func TestVersionsForFallsBackToEmptyWhenFetchOutlivesWait(t *testing.T) {
	origFetch := fetchVersions
	origWait := inflightWait
	t.Cleanup(func() {
		fetchVersions = origFetch
		inflightWait = origWait
	})
	inflightWait = 20 * time.Millisecond

	release := make(chan struct{})
	fetchVersions = func(dep string) ([]versionInfo, error) {
		<-release
		return []versionInfo{{version: "1.0.0", latest: true}}, nil
	}

	p := &npmVersionsPlugin{cache: make(map[string]cacheEntry), inflight: make(map[string]chan struct{})}

	if got := p.versionsFor("leftpad"); got != nil {
		t.Errorf("call during a slow in-flight fetch = %v, want nil", got)
	}
	close(release)

	// Poll briefly for the background fetch to land, rather than sleeping a
	// fixed duration.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := p.versionsFor("leftpad"); len(got) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("versionsFor never reflected the completed background fetch")
}

// TestRefreshKeepsStaleCacheOnFetchError verifies a transient registry
// failure doesn't wipe out previously-fetched (still-useful) suggestions.
func TestRefreshKeepsStaleCacheOnFetchError(t *testing.T) {
	origFetch := fetchVersions
	t.Cleanup(func() { fetchVersions = origFetch })
	fetchVersions = func(dep string) ([]versionInfo, error) {
		return nil, errors.New("network down")
	}

	p := &npmVersionsPlugin{
		cache: map[string]cacheEntry{
			"leftpad": {versions: []versionInfo{{version: "1.0.0"}}, fetched: time.Now().Add(-cacheTTL * 2)},
		},
		inflight: make(map[string]chan struct{}),
	}

	done := make(chan struct{})
	p.refresh("leftpad", done)
	<-done // refresh closes done itself; confirms it returned rather than hanging

	p.mu.Lock()
	entry := p.cache["leftpad"]
	_, stillInflight := p.inflight["leftpad"]
	p.mu.Unlock()
	if len(entry.versions) != 1 || entry.versions[0].version != "1.0.0" {
		t.Errorf("cache after failed refresh = %+v, want the stale entry preserved", entry)
	}
	if stillInflight {
		t.Error("inflight entry for leftpad was not cleaned up after refresh finished")
	}
}
