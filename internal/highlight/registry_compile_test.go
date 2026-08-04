package highlight

import (
	"testing"

	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

// TestAllRegisteredQueriesCompile ensures every language registered via
// registerLang has a highlight query that actually compiles against its
// grammar. New() silently returns nil on a compile failure (deliberately —
// one broken language shouldn't take down highlighting for every other file
// type), which means a broken query has no other signal short of a user
// noticing a file type quietly lost all its highlighting. Runs whatever
// languages the current build tags registered (empty and a no-op in a
// minimal build); run with -tags lang_all to check every language.
// knownBrokenQueries lists registerLang keys with a pre-existing,
// already-broken query, so this test documents the gap instead of silently
// dropping it. Remove an entry once its query compiles again.
var knownBrokenQueries = map[string]string{
	// go-sitter-forest's own vendored swift highlights.scm doesn't compile
	// against its own vendored grammar — a version-skew bug inside that
	// third-party module, not in indigo's query code. Confirmed present
	// before this test was added.
	".swift": "go-sitter-forest's bundled swift query/grammar are out of sync",
}

func TestAllRegisteredQueriesCompile(t *testing.T) {
	for key, fn := range langRegistry {
		if reason, known := knownBrokenQueries[key]; known {
			t.Logf("%s: skipping known-broken query (%s)", key, reason)
			continue
		}
		lang, qsrc := fn()
		if lang == nil || len(qsrc) == 0 {
			t.Errorf("%s: registerLang entry returned no language/query", key)
			continue
		}
		if _, err := sitter.NewQuery(lang, qsrc); err != nil {
			t.Errorf("%s: highlight query failed to compile: %v", key, err)
		}
	}
}
