//go:build (lang_all && !lang_not_python) || lang_python

package highlight

import (
	"bytes"

	"github.com/alexaandru/go-sitter-forest/python"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

// patchPythonHighlightQuery fixes a highlighting bug in tree-sitter-python's
// native highlights query: its "(string) @string" pattern captures a whole
// f-string's `string` node in its entirety, including any {...}
// interpolations. At priority 90 (see captureANSI) that outranks the
// lower-priority @variable/@punctuation.special captures the same query
// already assigns to identifiers/expressions inside an interpolation, since
// indigo resolves overlapping spans by flat priority rather than tree
// nesting (same class of bug as ecmaHighlightQuery's template_string --
// see query_ecma.go). Scoping @string down to the string's delimiter/
// content children, leaving `interpolation` uncaptured by @string, fixes
// it. Verified against go-sitter-forest's python grammar that every
// `string` node (plain, f-, raw, byte, triple-quoted) decomposes into
// string_start/string_content/string_end children uniformly.
func patchPythonHighlightQuery(raw []byte) []byte {
	old := []byte("(string) @string")
	patched := []byte(`[
  (string_start)
  (string_content)
  (string_end)
] @string`)
	return bytes.Replace(raw, old, patched, 1)
}

func init() {
	fn := func() (*sitter.Language, []byte) {
		// Use NativeFirst: the default nvimts query relies on Neovim-specific
		// #lua-match? predicates. go-tree-sitter-bare accepts them at compile
		// time but silently never enforces them (unlike markdown_inline's
		// #set! predicates, which fail to compile at all -- see that file's
		// comment for the sibling case of this same nvimts-vs-native split),
		// so every "((identifier) @type (#lua-match? ...))"-style rule in the
		// nvimts query matches unconditionally. That's what let every
		// plain-old identifier get @type/@constant/@constant.builtin spans
		// alongside its correct @variable one, with @constant.builtin's
		// higher priority winning. The native query uses #match? (a real
		// regex, which indigo's predicate evaluator does enforce) instead.
		return sitter.NewLanguage(python.GetLanguage()), patchPythonHighlightQuery(python.GetQuery("highlights", python.NativeFirst))
	}
	registerLang(fn, ".py")
	registerIndentQuery(func() []byte { return python.GetQuery("indents") }, ".py")
}
