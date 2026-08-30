package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/client9/gospell"
)

// TestSpellCheckerConcurrentAccess is a regression test for a data race one
// level deeper than TestSpellUserWordsConcurrentAccess (userwords_test.go):
// that test seeds its probe word into s.userWords so spell() short-circuits
// before ever touching s.checker. Here the probe word is deliberately *not*
// in userWords, so spell() falls through to s.checker.Spell — gospell's
// GoSpell.Spell/Suggest read its underlying dict, a plain map with zero
// internal synchronization, while addUserWord's s.checker.AddWordRaw writes
// to that same map. Before spell()/getFixes()/applyFix() took s.mu around
// their checker calls, a concurrent dictionary-add (applyFix,
// cmdAddGlobal/cmdAddWorkspace) racing a spell check or a fix-suggestion
// fetch was a genuine unsynchronized Go map read/write — fatal, not
// recoverable, and kills the whole plugin process rather than just one
// request. Run with -race: fails on the pre-fix implementation.
func TestSpellCheckerConcurrentAccess(t *testing.T) {
	s := newTestSpell()
	checker, err := gospell.NewGoSpellReader(bytes.NewReader(affData), bytes.NewReader(dicData))
	if err != nil {
		t.Fatalf("load dictionary: %v", err)
	}
	s.checker = checker
	// "wombat" is a real dictionary word but is not seeded into userWords, so
	// spell() must fall through to s.checker.Spell on every call.
	if !checker.Spell("wombat") {
		t.Fatal("test dictionary doesn't contain the probe word \"wombat\" — pick a different one")
	}

	var wg sync.WaitGroup
	for i := range 200 {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			s.mu.Lock()
			s.addUserWord(fmt.Sprintf("customword%d", i))
			s.mu.Unlock()
		}(i)
		go func() {
			defer wg.Done()
			if !s.spell("wombat") {
				t.Error(`spell("wombat") = false, want true`)
			}
		}()
		go func() {
			defer wg.Done()
			payload, _ := json.Marshal(fixPayload{Word: "wombta"}) // misspelling, to force a Suggest call
			_ = s.getFixes(string(payload))
		}()
	}
	wg.Wait()
}
