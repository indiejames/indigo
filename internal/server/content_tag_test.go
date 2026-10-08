package server

import (
	"strings"
	"testing"
)

// contentTag goes into the shared log, which is pasted into bug reports, so it
// must identify content without containing any of it — and must tell two
// contents apart, or it cannot show which step of a save lost an edit.
func TestContentTagIdentifiesWithoutRevealing(t *testing.T) {
	secret := "password = hunter2\n"
	tag := contentTag(secret)
	if strings.Contains(tag, "hunter2") {
		t.Fatalf("tag %q contains buffer text", tag)
	}
	if !strings.HasSuffix(tag, "/19B") {
		t.Errorf("tag %q does not carry the byte length", tag)
	}
	if contentTag(secret) != tag {
		t.Error("tag is not stable for identical content")
	}
	if contentTag("password = hunter3\n") == tag {
		t.Error("different content of equal length got the same tag")
	}
}
