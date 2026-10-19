package client

import "testing"

// TestPluginMenuItemCannotTakeABuiltInKey: plugins reach their actions only
// through the Command (space) menu, and there too the editor's own entries
// come first. A plugin item declaring a key a built-in entry already uses is
// shadowed by it, so a plugin cannot take over a key there either.
func TestPluginMenuItemCannotTakeABuiltInKey(t *testing.T) {
	plugin := clientMenuItemsToCommands([]ClientMenuItem{
		{Key: "s", Label: "Steal search", PluginName: "thief", Command: "thief.s"},
		{Key: "z", Label: "Own key", PluginName: "thief", Command: "thief.z"},
	})
	menu := spaceMenuWith(plugin)

	got, ok := findIn(menu.children, []string{"s"})
	if !ok || got.name != "search-and-replace" {
		t.Errorf("space s resolves to %+v, want the built-in search-and-replace", got)
	}
	got, ok = findIn(menu.children, []string{"z"})
	if !ok || got.label != "Own key" {
		t.Errorf("space z resolves to %+v, want the plugin's own entry", got)
	}
}

// TestHelpOpensWithoutAskingTheServer: ? used to fetch plugin key bindings
// before opening the help popup. Plugins bind no keys now, so it opens at once
// and lists the editor's own bindings.
func TestHelpOpensWithoutAskingTheServer(t *testing.T) {
	m := newTestModel("x\n")
	updated, cmd := executeShowHelp(m)
	got := updated.(Model)
	if !got.helpVisible || got.helpScroll != 0 {
		t.Errorf("help visible %v, scroll %d; want it open at the top", got.helpVisible, got.helpScroll)
	}
	if cmd != nil {
		t.Error("opening help returned a command; it must not need the server")
	}
	if lines := helpPopupLines(helpPopupInnerWidth(80)); len(lines) == 0 {
		t.Error("the help popup has no lines")
	}
}
