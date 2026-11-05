package client

import "testing"

// TestDigitsAskForTheirTab: in Normal mode 1 to 8 ask the App for that tab
// and 9 for the last one; 0 stays go-to-line-start.
func TestDigitsAskForTheirTab(t *testing.T) {
	for _, key := range []string{"1", "2", "8", "9"} {
		m := newTestModel("hello\n")
		_, cmd := m.handleKey(fakeKey(key))
		if cmd == nil {
			t.Fatalf("%s: no command", key)
		}
		got, ok := cmd().(GoToTabMsg)
		if want := int(key[0] - '0'); !ok || got.N != want {
			t.Errorf("%s: sent %#v, want GoToTabMsg{N: %d}", key, cmd(), want)
		}
	}
}
