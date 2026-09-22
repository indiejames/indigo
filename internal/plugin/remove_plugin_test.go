package plugin

import "testing"

// TestRemovePluginByIdentity is the regression for startPlugin removing a
// plugin whose Initialize failed by the index it was appended at. Plugins start
// concurrently, so an earlier plugin failing first shifted that index onto a
// different plugin, and Shutdown clearing the list while an Initialize was
// still waiting made it panic with "slice bounds out of range".
func TestRemovePluginByIdentity(t *testing.T) {
	a, b, c := &registeredPlugin{name: "a"}, &registeredPlugin{name: "b"}, &registeredPlugin{name: "c"}
	m := &Manager{plugins: []*registeredPlugin{a, b, c}}

	// b was appended at index 1; a is removed first, as if it failed first.
	m.removePlugin(a)
	m.removePlugin(b)
	if len(m.plugins) != 1 || m.plugins[0] != c {
		names := []string{}
		for _, p := range m.plugins {
			names = append(names, p.name)
		}
		t.Fatalf("plugins = %v, want [c]", names)
	}

	// Shutdown cleared the list while an Initialize was in flight.
	m.plugins = nil
	m.removePlugin(c) // must not panic
}
