//go:build (lang_all && !lang_not_rust) || lang_rust

package highlight

import (
	"context"
	"strings"

	"github.com/alexaandru/go-sitter-forest/rust"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(rust.GetLanguage()), rust.GetQuery("highlights")
	}
	registerLang(fn, ".rs")
	registerIndentQuery(func() []byte { return rust.GetQuery("indents") }, ".rs")
	registerPostProcess(rustFormatArgSpans, ".rs")
}

// rustFormatMacros lists the std/core formatting macros whose string-literal
// arguments follow std::fmt syntax and so may contain captured-identifier
// interpolations like `{name}` (stabilized in Rust 2021). Not exhaustive of
// every macro anyone might define, but covers the ones anyone actually
// writes `{ident}` inside.
var rustFormatMacros = map[string]bool{
	"format": true, "print": true, "println": true,
	"eprint": true, "eprintln": true,
	"write": true, "writeln": true, "format_args": true,
	"panic": true, "unreachable": true, "todo": true, "unimplemented": true,
	"assert": true, "assert_eq": true, "assert_ne": true,
	"debug_assert": true, "debug_assert_eq": true, "debug_assert_ne": true,
}

// rustFormatStringQuery finds string_content nodes that are the direct
// content of a plain (non-raw) string_literal passed as an argument to one
// of rustFormatMacros. Whether the macro name matches is checked in Go
// against the captured @macro node's text rather than via a query
// predicate, matching what indigo's predicate evaluator is actually known
// to support (see lang_python.go's patchPythonHighlightQuery for a case
// where trusting an unsupported predicate silently broke highlighting).
const rustFormatStringQuery = `
(macro_invocation
  macro: (identifier) @macro
  (token_tree
    (string_literal
      (string_content) @content)))
`

var rustFormatQuery *sitter.Query

func rustFormatArgSpans(content []byte) LineSpans {
	if rustFormatQuery == nil {
		lang := sitter.NewLanguage(rust.GetLanguage())
		q, err := sitter.NewQuery(lang, []byte(rustFormatStringQuery))
		if err != nil {
			return nil
		}
		rustFormatQuery = q
	}
	lang := sitter.NewLanguage(rust.GetLanguage())
	p := sitter.NewParser()
	p.SetLanguage(lang)
	tree, err := p.ParseString(context.Background(), nil, content)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()

	variableANSI, _, ok := captureANSI("variable")
	if !ok {
		return nil
	}

	lines := strings.Split(string(content), "\n")

	result := LineSpans{}
	qc := sitter.NewQueryCursor()
	matches := qc.Matches(rustFormatQuery, tree.RootNode(), content)
	for {
		m := matches.Next()
		if m == nil {
			break
		}
		var macroName string
		var contentNode sitter.Node
		haveContent := false
		for _, cap := range m.Captures {
			name := rustFormatQuery.CaptureNameForID(cap.Index)
			switch name {
			case "macro":
				macroName = string(content[cap.Node.StartByte():cap.Node.EndByte()])
			case "content":
				contentNode = cap.Node
				haveContent = true
			}
		}
		if !haveContent || !rustFormatMacros[macroName] {
			continue
		}
		result = mergeLineSpans(result, rustScanFormatArgs(contentNode, content, lines, variableANSI))
	}
	return result
}

// rustScanFormatArgs scans a single format string's raw content for
// `{identifier}` / `{identifier:spec}` captured-argument interpolations
// (std::fmt syntax: https://doc.rust-lang.org/std/fmt/index.html) and
// returns a Span for each identifier's own range. `{{`/`}}` are literal
// escaped braces, not interpolations; `{}`/`{0}` (empty or purely numeric —
// positional arguments) are deliberately not spans, since there's no
// identifier there to color. Dynamic width/precision references inside the
// format spec after `:` (e.g. the `width` in `{v:width$}`) are not handled.
func rustScanFormatArgs(node sitter.Node, content []byte, lines []string, ansi string) LineSpans {
	startRow := int(node.StartPoint().Row)
	startByteCol := int(node.StartPoint().Column)
	startCol := byteToRuneCol(lineAt(lines, startRow), startByteCol)

	text := []rune(string(content[node.StartByte():node.EndByte()]))
	row, col := startRow, startCol

	advance := func(r rune) {
		if r == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}

	result := LineSpans{}
	i := 0
	n := len(text)
	for i < n {
		c := text[i]
		if c == '{' {
			if i+1 < n && text[i+1] == '{' {
				advance(c)
				i++
				advance(text[i])
				i++
				continue
			}
			advance(c)
			i++
			argRow, argCol := row, col
			argStart := i
			for i < n && text[i] != '}' && text[i] != ':' {
				advance(text[i])
				i++
			}
			argEnd := i
			for i < n && text[i] != '}' {
				advance(text[i])
				i++
			}
			if i < n { // closing '}'
				advance(text[i])
				i++
			}
			arg := string(text[argStart:argEnd])
			if isRustIdentifier(arg) {
				result[argRow] = append(result[argRow], Span{StartCol: argCol, EndCol: argCol + (argEnd - argStart), ANSI: ansi})
			}
			continue
		}
		if c == '}' && i+1 < n && text[i+1] == '}' {
			advance(c)
			i++
			advance(text[i])
			i++
			continue
		}
		advance(c)
		i++
	}
	return result
}

func isRustIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case i == 0 && (r == '_' || isRustIDLetter(r)):
		case i > 0 && (r == '_' || isRustIDLetter(r) || (r >= '0' && r <= '9')):
		default:
			return false
		}
	}
	return true
}

func isRustIDLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func mergeLineSpans(a, b LineSpans) LineSpans {
	for line, spans := range b {
		a[line] = append(a[line], spans...)
	}
	return a
}
