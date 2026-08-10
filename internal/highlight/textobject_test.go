//go:build lang_go || lang_all

package highlight

import (
	"strings"
	"testing"
)

// TestTextObjectAtArgumentMultiByteUTF8 is a regression test: TextObjectAt
// used to pass the caller's rune-based cursor column straight to
// tree-sitter as a byte column with no conversion, so on any line with
// multi-byte UTF-8 before the cursor, the wrong argument (or none) was
// selected. The fixture's first argument is packed with multi-byte runes so
// the byte/rune offsets diverge well before the cursor's target position.
func TestTextObjectAtArgumentMultiByteUTF8(t *testing.T) {
	h := New("test.go")
	if h == nil {
		t.Skip("no go highlighter registered")
	}
	content := []byte("package main\n\nfunc main() {\n\tfmt.Println(\"héllo wörld café mörning\", \"zebra\")\n}\n")
	lines := strings.Split(string(content), "\n")
	lineIdx := 3
	line := lines[lineIdx]

	target := `"zebra"`
	byteIdx := strings.Index(line, target)
	if byteIdx < 0 {
		t.Fatal("target substring not found in fixture")
	}
	// The rune-based column a couple runes into the target — what the
	// client would actually send as the cursor position.
	col := byteToRuneCol(line, byteIdx+2)

	obj, ok := h.TextObjectAt(content, lineIdx, col, "argument")
	if !ok {
		t.Fatal("TextObjectAt(argument) = false, want true")
	}
	if obj.StartLine != lineIdx || obj.EndLine != lineIdx {
		t.Fatalf("obj spans lines %d-%d, want single line %d", obj.StartLine, obj.EndLine, lineIdx)
	}
	runes := []rune(line)
	if obj.StartCol < 0 || obj.EndCol > len(runes) || obj.StartCol > obj.EndCol {
		t.Fatalf("obj cols [%d:%d] out of range for line of %d runes", obj.StartCol, obj.EndCol, len(runes))
	}
	got := string(runes[obj.StartCol:obj.EndCol])
	if got != target {
		t.Errorf("selected argument = %q, want %q (cols [%d:%d] resolved to the wrong node)", got, target, obj.StartCol, obj.EndCol)
	}
}

// TestTextObjectAroundCommentMultiByteUTF8 is the TextObjectAround
// counterpart: multi-byte runes earlier on the line (in a string literal)
// used to throw off the byte column tree-sitter needs, landing the lookup
// point outside the comment node entirely.
func TestTextObjectAroundCommentMultiByteUTF8(t *testing.T) {
	h := New("test.go")
	if h == nil {
		t.Skip("no go highlighter registered")
	}
	content := []byte("package main\n\nvar héllo = \"wörld\" // café comment blöck\n")
	lines := strings.Split(string(content), "\n")
	lineIdx := 2
	line := lines[lineIdx]

	target := "// café comment blöck"
	byteIdx := strings.Index(line, target)
	if byteIdx < 0 {
		t.Fatal("target substring not found in fixture")
	}
	col := byteToRuneCol(line, byteIdx+5) // a few runes into the comment

	obj, ok := h.TextObjectAround(content, lineIdx, col, "comment")
	if !ok {
		t.Fatal("TextObjectAround(comment) = false, want true")
	}
	if obj.StartLine != lineIdx || obj.EndLine != lineIdx {
		t.Fatalf("obj spans lines %d-%d, want single line %d", obj.StartLine, obj.EndLine, lineIdx)
	}
	runes := []rune(line)
	if obj.StartCol < 0 || obj.EndCol > len(runes) || obj.StartCol > obj.EndCol {
		t.Fatalf("obj cols [%d:%d] out of range for line of %d runes", obj.StartCol, obj.EndCol, len(runes))
	}
	got := string(runes[obj.StartCol:obj.EndCol])
	if got != target {
		t.Errorf("selected comment = %q, want %q (cols [%d:%d] resolved to the wrong node)", got, target, obj.StartCol, obj.EndCol)
	}
}

// TestTextObjectAtFunctionASCIIBaseline is a plain-ASCII sanity check for
// the "function" kind (select-inside a function body) alongside the
// multi-byte cases above, since TextObjectAt/TextObjectAround had no direct
// tests at all before this file.
func TestTextObjectAtFunctionASCIIBaseline(t *testing.T) {
	h := New("test.go")
	if h == nil {
		t.Skip("no go highlighter registered")
	}
	content := []byte("package main\n\nfunc greet() string {\n\treturn \"hi\"\n}\n")

	// Cursor inside the function body (line 3, "return \"hi\"").
	obj, ok := h.TextObjectAt(content, 3, 4, "function")
	if !ok {
		t.Fatal("TextObjectAt(function) = false, want true")
	}
	if obj.StartLine != 3 || obj.EndLine != 3 {
		t.Errorf("obj spans lines %d-%d, want the single-statement body line 3", obj.StartLine, obj.EndLine)
	}
}
