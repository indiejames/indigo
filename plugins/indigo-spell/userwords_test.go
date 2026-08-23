package main

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/client9/gospell"
)

// TestSpellUserWordsConcurrentAccess is a regression test for a data race:
// spell() used to read s.userWords with no locking while addUserWord (called
// from the :spell-add/:spell-add-workspace command handlers, the fix-popup
// "add to dictionary" action, and — since the workspace-scan feature —
// runWorkspaceScan's own goroutine) writes to it under s.mu. "apple" is
// seeded before the concurrent phase so every spell("apple") call
// short-circuits on the userWords hit without reaching the (nil, in this
// unit test) checker. Run with -race: it fails on the pre-fix
// implementation (unsynchronized map read racing a locked map write) and
// passes once spell() takes s.mu around the userWords lookup.
func TestSpellUserWordsConcurrentAccess(t *testing.T) {
	s := newTestSpell()
	checker, err := gospell.NewGoSpellReader(bytes.NewReader(affData), bytes.NewReader(dicData))
	if err != nil {
		t.Fatalf("load dictionary: %v", err)
	}
	s.checker = checker
	s.addUserWord("apple")

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			s.mu.Lock()
			s.addUserWord(fmt.Sprintf("word%d", i))
			s.mu.Unlock()
		}(i)
		go func() {
			defer wg.Done()
			if !s.spell("apple") {
				t.Error(`spell("apple") = false, want true (seeded before the concurrent phase)`)
			}
		}()
	}
	wg.Wait()
}
