//go:build (lang_all && !lang_not_rust) || lang_rust

package highlight

import (
	"context"
	"strings"
	"sync"
	"unicode"

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

// rustFormatMacroArgPos lists the std/core formatting macros whose
// std::fmt-syntax format string (the argument that may contain
// captured-identifier interpolations like `{name}`, stabilized in Rust
// 2021) sits at a fixed, 0-indexed position among the macro's top-level
// comma-separated arguments. Most take the format string first, but
// write!/writeln! take a destination writer first (format string at
// position 1), and assert!/debug_assert! take a boolean condition first
// (format string, if present at all, at position 1); assert_eq!/
// assert_ne! and their debug_ variants take two values to compare before
// an optional format string at position 2. Not exhaustive of every macro
// anyone might define, but covers the ones anyone actually writes
// `{ident}` inside.
var rustFormatMacroArgPos = map[string]int{
	"print": 0, "println": 0, "eprint": 0, "eprintln": 0,
	"format": 0, "format_args": 0,
	"panic": 0, "unreachable": 0, "todo": 0, "unimplemented": 0,
	"write": 1, "writeln": 1,
	"assert": 1, "debug_assert": 1,
	"assert_eq": 2, "assert_ne": 2, "debug_assert_eq": 2, "debug_assert_ne": 2,
}

// rustFormatStringQuery finds macro_invocation nodes and their whole
// token_tree of arguments; rustFormatStringNode then picks out the specific
// argument that's actually the format string (see rustFormatMacroArgPos),
// so a later argument that merely happens to be a string literal containing
// literal `{...}` text (e.g. the second, value argument of
// `println!("{}", "literal {key}")`) isn't mistaken for one. Whether the
// macro name matches is checked in Go against the captured @macro node's
// text rather than via a query predicate, matching what indigo's predicate
// evaluator is actually known to support (see lang_python.go's
// patchPythonHighlightQuery for a case where trusting an unsupported
// predicate silently broke highlighting).
const rustFormatStringQuery = `
(macro_invocation
  macro: (identifier) @macro
  (token_tree) @args)
`

var (
	rustFormatOnce  sync.Once
	rustFormatQuery *sitter.Query
	rustFormatLang  *sitter.Language
)

func rustFormatInit() {
	rustFormatLang = sitter.NewLanguage(rust.GetLanguage())
	q, err := sitter.NewQuery(rustFormatLang, []byte(rustFormatStringQuery))
	if err != nil {
		return
	}
	rustFormatQuery = q
}

func rustFormatArgSpans(content []byte) LineSpans {
	rustFormatOnce.Do(rustFormatInit)
	if rustFormatQuery == nil || rustFormatLang == nil {
		return nil
	}
	p := sitter.NewParser()
	p.SetLanguage(rustFormatLang)
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
		var argsNode sitter.Node
		haveArgs := false
		for _, cap := range m.Captures {
			name := rustFormatQuery.CaptureNameForID(cap.Index)
			switch name {
			case "macro":
				macroName = string(content[cap.Node.StartByte():cap.Node.EndByte()])
			case "args":
				argsNode = cap.Node
				haveArgs = true
			}
		}
		if !haveArgs {
			continue
		}
		argPos, ok := rustFormatMacroArgPos[macroName]
		if !ok {
			continue
		}
		contentNode, ok := rustFormatStringNode(argsNode, argPos)
		if !ok {
			continue
		}
		result = mergeLineSpans(result, rustScanFormatArgs(contentNode, content, lines, variableANSI))
	}
	return result
}

// rustFormatStringNode returns the string_content of the string_literal
// found in the argPos'th top-level, comma-separated argument of a macro's
// token_tree (its direct children — top-level because a parenthesized/
// bracketed/braced sub-expression parses as its own nested token_tree, so
// its internal commas are that nested node's children, not tokenTree's).
func rustFormatStringNode(tokenTree sitter.Node, argPos int) (sitter.Node, bool) {
	argIdx := 0
	var found sitter.Node
	haveFound := false
	n := tokenTree.ChildCount()
	for i := uint32(0); i < n; i++ {
		c := tokenTree.Child(i)
		if c.Type() == "," {
			if argIdx == argPos {
				return found, haveFound
			}
			argIdx++
			haveFound = false
			continue
		}
		if argIdx != argPos || haveFound || c.Type() != "string_literal" {
			continue
		}
		for j := uint32(0); j < c.NamedChildCount(); j++ {
			nc := c.NamedChild(j)
			if nc.Type() == "string_content" {
				found = nc
				haveFound = true
				break
			}
		}
	}
	if argIdx == argPos {
		return found, haveFound
	}
	return sitter.Node{}, false
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

// isRustIdentifier reports whether s is a valid Rust identifier for
// std::fmt captured-argument purposes: unicode.IsLetter/IsDigit is an
// approximation of Rust's actual XID_Start/XID_Continue identifier rules
// (which allow Unicode letters, not just ASCII), close enough for
// highlighting purposes. An optional "r#" raw-identifier prefix (used to
// name a variable after a keyword, e.g. `let r#match = ...`) is accepted
// and stripped before validating the rest, since rustc's format-string
// argument parser recognizes it too.
func isRustIdentifier(s string) bool {
	s = strings.TrimPrefix(s, "r#")
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case i == 0 && (r == '_' || unicode.IsLetter(r)):
		case i > 0 && (r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)):
		default:
			return false
		}
	}
	return true
}

func mergeLineSpans(a, b LineSpans) LineSpans {
	for line, spans := range b {
		a[line] = append(a[line], spans...)
	}
	return a
}
