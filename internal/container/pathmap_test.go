package container

import "testing"

func TestPathMapTranslatesBothWays(t *testing.T) {
	m := NewPathMap("/Users/me/proj", "/workspaces/proj")

	got, ok := m.ToContainer("/Users/me/proj/cmd/main.go")
	if !ok || got != "/workspaces/proj/cmd/main.go" {
		t.Errorf("ToContainer = (%q, %v), want (/workspaces/proj/cmd/main.go, true)", got, ok)
	}
	got, ok = m.ToHost("/workspaces/proj/cmd/main.go")
	if !ok || got != "/Users/me/proj/cmd/main.go" {
		t.Errorf("ToHost = (%q, %v), want (/Users/me/proj/cmd/main.go, true)", got, ok)
	}
}

func TestPathMapTranslatesTheRootItself(t *testing.T) {
	m := NewPathMap("/Users/me/proj", "/workspaces/proj")
	if got, ok := m.ToContainer("/Users/me/proj"); !ok || got != "/workspaces/proj" {
		t.Errorf("ToContainer(root) = (%q, %v), want (/workspaces/proj, true)", got, ok)
	}
	// A trailing slash names the same directory.
	if got, ok := m.ToContainer("/Users/me/proj/"); !ok || got != "/workspaces/proj" {
		t.Errorf("ToContainer(root+/) = (%q, %v), want (/workspaces/proj, true)", got, ok)
	}
}

// TestPathMapDoesNotMatchASiblingWithTheSamePrefix is the one a naive
// strings.HasPrefix gets wrong: "proj" and "project" are different directories,
// and mapping the second into the first produces a path that exists and holds
// someone else's file.
func TestPathMapDoesNotMatchASiblingWithTheSamePrefix(t *testing.T) {
	m := NewPathMap("/Users/me/proj", "/workspaces/proj")

	got, ok := m.ToContainer("/Users/me/project/main.go")
	if ok {
		t.Errorf("ToContainer matched a sibling directory, giving %q", got)
	}
	if got != "/Users/me/project/main.go" {
		t.Errorf("an untranslated path came back changed: %q", got)
	}
}

// TestPathMapLeavesOutsidePathsAlone: a file that is not mounted has no
// container path, and inventing one turns "not visible here" into a failure
// further in, with a path nobody typed.
func TestPathMapLeavesOutsidePathsAlone(t *testing.T) {
	m := NewPathMap("/Users/me/proj", "/workspaces/proj")
	for _, p := range []string{"/etc/hosts", "/Users/me/other/a.go", "/Users/me"} {
		got, ok := m.ToContainer(p)
		if ok {
			t.Errorf("ToContainer(%q) claimed a translation: %q", p, got)
		}
		if got != p {
			t.Errorf("ToContainer(%q) = %q, want it unchanged", p, got)
		}
	}
}

func TestPathMapIdentity(t *testing.T) {
	// The `-v $PWD:$PWD` case: nothing to do.
	m := NewPathMap("/Users/me/proj", "/Users/me/proj")
	if !m.Identity() {
		t.Error("identical roots did not report Identity")
	}
	if got, ok := m.ToContainer("/Users/me/proj/a.go"); !ok || got != "/Users/me/proj/a.go" {
		t.Errorf("ToContainer = (%q, %v), want the path unchanged and translated", got, ok)
	}
	if NewPathMap("/a", "/b").Identity() {
		t.Error("different roots reported Identity")
	}
}

func TestPathMapNormalisesRoots(t *testing.T) {
	m := NewPathMap("/Users/me/proj/", "/workspaces/proj/./")
	if m.HostRoot != "/Users/me/proj" || m.ContainerRoot != "/workspaces/proj" {
		t.Errorf("roots = %q, %q; want them cleaned", m.HostRoot, m.ContainerRoot)
	}
	if got, ok := m.ToContainer("/Users/me/proj/a.go"); !ok || got != "/workspaces/proj/a.go" {
		t.Errorf("ToContainer = (%q, %v)", got, ok)
	}
}

// TestPathMapIsInertWithoutRoots covers the un-attached case: with no container
// there is no mapping, and every path has to come through untouched rather than
// being rewritten against an empty root.
func TestPathMapIsInertWithoutRoots(t *testing.T) {
	for _, m := range []PathMap{{}, NewPathMap("/Users/me/proj", ""), NewPathMap("", "/workspaces/proj"), NewPathMap("/", "/")} {
		if got, ok := m.ToContainer("/Users/me/proj/a.go"); ok || got != "/Users/me/proj/a.go" {
			t.Errorf("%+v ToContainer = (%q, %v), want the path unchanged and untranslated", m, got, ok)
		}
		if got, ok := m.ToHost("/workspaces/proj/a.go"); ok || got != "/workspaces/proj/a.go" {
			t.Errorf("%+v ToHost = (%q, %v), want the path unchanged and untranslated", m, got, ok)
		}
	}
}

// TestPathMapRoundTrips because a path that survives one direction but not the
// other would show up as a file that opens and then cannot be saved.
func TestPathMapRoundTrips(t *testing.T) {
	m := NewPathMap("/Users/me/proj", "/workspaces/proj")
	for _, p := range []string{
		"/Users/me/proj",
		"/Users/me/proj/a.go",
		"/Users/me/proj/deep/nested/dir/file.txt",
	} {
		inContainer, ok := m.ToContainer(p)
		if !ok {
			t.Fatalf("ToContainer(%q) did not translate", p)
		}
		back, ok := m.ToHost(inContainer)
		if !ok || back != p {
			t.Errorf("round trip of %q gave (%q, %v)", p, back, ok)
		}
	}
}
