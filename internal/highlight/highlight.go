package highlight

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

// Span marks a highlighted range on a single line.
// StartCol and EndCol are rune-based; EndCol is exclusive.
// EndCol == math.MaxInt means "to end of line".
// ANSI is the opening SGR escape sequence; close with "\x1b[m".
type Span struct {
	StartCol int
	EndCol   int
	ANSI     string
}

// ANSIReset is the sequence that closes any open Span.ANSI.
const ANSIReset = "\x1b[m"

// LineSpans maps line number → slice of Spans sorted by priority (highest first).
type LineSpans map[int][]Span

// Highlighter holds the compiled language and query for a file type.
type Highlighter struct {
	lang  *sitter.Language
	query *sitter.Query
	extra *Highlighter // optional secondary parser (e.g. markdown_inline)
}

// New returns a Highlighter for filePath, or nil if the language is unsupported.
func New(filePath string) *Highlighter {
	lang, qsrc := languageForPath(filePath)
	if lang == nil || len(qsrc) == 0 {
		return nil
	}
	q, err := sitter.NewQuery(lang, qsrc)
	if err != nil {
		return nil
	}
	h := &Highlighter{lang: lang, query: q}
	if elang, eqsrc := extraLanguageForPath(filePath); elang != nil && len(eqsrc) > 0 {
		if eq, err := sitter.NewQuery(elang, eqsrc); err == nil {
			h.extra = &Highlighter{lang: elang, query: eq}
		}
	}
	return h
}

// Highlight parses content and returns highlighted spans for all lines.
// A fresh parser is created each call so it is safe to call concurrently.
func (h *Highlighter) Highlight(content []byte) LineSpans {
	if h == nil {
		return nil
	}
	p := sitter.NewParser()
	p.SetLanguage(h.lang)
	tree, err := p.ParseString(context.Background(), nil, content)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()
	result := extractSpans(h.query, tree, content)
	if h.extra != nil {
		for line, spans := range h.extra.Highlight(content) {
			result[line] = append(result[line], spans...)
		}
	}
	return result
}

// --- span extraction ---

type rawSpan struct {
	line     int
	startCol int
	endCol   int
	ansi     string
	priority int
}

func extractSpans(query *sitter.Query, tree *sitter.Tree, content []byte) LineSpans {
	lines := strings.Split(string(content), "\n")

	var raw []rawSpan
	qc := sitter.NewQueryCursor()
	matches := qc.Matches(query, tree.RootNode(), content)
	for {
		m := matches.Next()
		if m == nil {
			break
		}
		for _, cap := range m.Captures {
			name := query.CaptureNameForID(cap.Index)
			ansi, prio, ok := captureANSI(name)
			if !ok {
				continue
			}
			node := cap.Node
			startRow := int(node.StartPoint().Row)
			endRow := int(node.EndPoint().Row)
			startCol := byteToRuneCol(lineAt(lines, startRow), int(node.StartPoint().Column))

			if startRow == endRow {
				endCol := byteToRuneCol(lineAt(lines, endRow), int(node.EndPoint().Column))
				raw = append(raw, rawSpan{startRow, startCol, endCol, ansi, prio})
			} else {
				raw = append(raw, rawSpan{startRow, startCol, math.MaxInt, ansi, prio})
				for row := startRow + 1; row < endRow; row++ {
					raw = append(raw, rawSpan{row, 0, math.MaxInt, ansi, prio})
				}
				endCol := byteToRuneCol(lineAt(lines, endRow), int(node.EndPoint().Column))
				raw = append(raw, rawSpan{endRow, 0, endCol, ansi, prio})
			}
		}
	}

	// Highest priority first so the first match in renderLineRunes wins.
	sort.SliceStable(raw, func(i, j int) bool {
		return raw[i].priority > raw[j].priority
	})

	result := make(LineSpans)
	for _, s := range raw {
		result[s.line] = append(result[s.line], Span{s.startCol, s.endCol, s.ansi})
	}
	return result
}

func lineAt(lines []string, n int) string {
	if n < 0 || n >= len(lines) {
		return ""
	}
	return lines[n]
}

func byteToRuneCol(line string, byteCol int) int {
	if byteCol <= 0 {
		return 0
	}
	if byteCol >= len(line) {
		return len([]rune(line))
	}
	return len([]rune(line[:byteCol]))
}

// --- capture → ANSI mapping ---

// hexToANSI converts a "#RRGGBB" hex color to a true-color SGR open sequence.
func hexToANSI(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	r, _ := strconv.ParseInt(hex[0:2], 16, 64)
	g, _ := strconv.ParseInt(hex[2:4], 16, 64)
	b, _ := strconv.ParseInt(hex[4:6], 16, 64)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

// hexToANSIBold returns a bold+color SGR sequence.
func hexToANSIBold(hex string) string {
	return "\x1b[1m" + hexToANSI(hex)
}

// hexToANSIItalic returns an italic+color SGR sequence.
func hexToANSIItalic(hex string) string {
	return "\x1b[3m" + hexToANSI(hex)
}

type captureEntry struct {
	ansi     string
	priority int
}

// SyntaxStyle is the theme-facing description of a token style.
// Defined here (rather than importing the theme package) to avoid a circular import.
type SyntaxStyle struct {
	Fg     string
	Bold   bool
	Italic bool
}

// defaultCaptureTable holds the built-in default-dark colors and is never modified.
var defaultCaptureTable = map[string]captureEntry{
	"comment":               {hexToANSI("#6A9955"), 100},
	"string":                {hexToANSI("#CE9178"), 90},
	"string.special":        {hexToANSI("#D7BA7D"), 88},
	"number":                {hexToANSI("#B5CEA8"), 80},
	"boolean":               {hexToANSI("#569CD6"), 78},
	"constant.builtin":      {hexToANSI("#569CD6"), 75},
	"constant":              {hexToANSI("#9CDCFE"), 72},
	"keyword":               {hexToANSI("#C586C0"), 70},
	"keyword.builtin":       {hexToANSI("#569CD6"), 70},
	"keyword.operator":      {hexToANSI("#C586C0"), 69},
	"keyword.return":        {hexToANSI("#C586C0"), 69},
	"keyword.import":        {hexToANSI("#C586C0"), 69},
	"operator":              {hexToANSI("#D4D4D4"), 65},
	"function":              {hexToANSI("#DCDCAA"), 60},
	"function.call":         {hexToANSI("#DCDCAA"), 55},
	"function.builtin":      {hexToANSI("#DCDCAA"), 55},
	"function.method":       {hexToANSI("#DCDCAA"), 55},
	"function.method.call":  {hexToANSI("#DCDCAA"), 54},
	"type":                  {hexToANSI("#4EC9B0"), 50},
	"type.builtin":          {hexToANSI("#4EC9B0"), 50},
	"namespace":             {hexToANSI("#4EC9B0"), 48},
	"module":                {hexToANSI("#4EC9B0"), 48},
	"tag":                   {hexToANSI("#569CD6"), 46},
	"attribute":             {hexToANSI("#9CDCFE"), 44},
	"variable.builtin":      {hexToANSI("#569CD6"), 42},
	"variable":              {hexToANSI("#9CDCFE"), 40},
	"label":                 {hexToANSI("#9CDCFE"), 38},
	"punctuation.bracket":   {hexToANSI("#D4D4D4"), 20},
	"punctuation.delimiter": {hexToANSI("#D4D4D4"), 20},
	"punctuation.special":   {hexToANSI("#C586C0"), 20},

	// markup.* — used by markdown and other markup grammars (modern nvim-treesitter naming).
	// captureANSI only prefix-matches on the first dot, so markup.heading.1 falls back to
	// the "markup" base entry; specific subtype entries override that via exact match.
	"markup":               {hexToANSI("#D4D4D4"), 28},
	"markup.heading":       {hexToANSI("#569CD6"), 87},
	"markup.heading.1":     {hexToANSI("#4FC1FF"), 87},
	"markup.heading.2":     {hexToANSI("#569CD6"), 86},
	"markup.heading.3":     {hexToANSI("#4EC9B0"), 85},
	"markup.heading.4":     {hexToANSI("#9CDCFE"), 84},
	"markup.heading.5":     {hexToANSI("#9CDCFE"), 84},
	"markup.heading.6":     {hexToANSI("#9CDCFE"), 84},
	"markup.strong":        {hexToANSIBold("#E5C07B"), 83},
	"markup.italic":        {hexToANSIItalic("#D7BA7D"), 83},
	"markup.strikethrough": {hexToANSI("#808080"), 83},
	"markup.raw":           {hexToANSI("#ABC2A1"), 82},
	"markup.raw.block":     {hexToANSI("#ABC2A1"), 82},
	"markup.link":          {hexToANSI("#615bd3"), 78},
	"markup.link.url":      {hexToANSI("#615bd3"), 78},
	"markup.link.label":    {hexToANSI("#9CDCFE"), 76},
	"markup.list":          {hexToANSI("#C586C0"), 76},
	"markup.quote":         {hexToANSI("#6A9955"), 74},

	// text.* — old nvim-treesitter naming still used by some grammars.
	"text":          {hexToANSI("#D4D4D4"), 27},
	"text.title":    {hexToANSI("#569CD6"), 87},
	"text.strong":   {hexToANSIBold("#E5C07B"), 83},
	"text.emphasis": {hexToANSIItalic("#D7BA7D"), 83},
	"text.literal":  {hexToANSI("#d5890e"), 82},
	"text.uri":      {hexToANSI("#615bd3"), 78},
	"text.reference": {hexToANSI("#9CDCFE"), 76},
}

// captureTable is the active mapping, rebuilt by ApplyTheme.
// Starts as a copy of defaultCaptureTable (set in init).
var captureTable map[string]captureEntry

func init() {
	captureTable = make(map[string]captureEntry, len(defaultCaptureTable))
	for k, v := range defaultCaptureTable {
		captureTable[k] = v
	}
}

// ApplyTheme rebuilds the active syntax color table from the theme's syntax map.
// Scopes absent from the theme keep their default-dark colors; scopes present
// in the theme override only the color, preserving the built-in priority.
func ApplyTheme(syntax map[string]SyntaxStyle) {
	updated := make(map[string]captureEntry, len(defaultCaptureTable))
	for k, v := range defaultCaptureTable {
		updated[k] = v
	}
	for scope, style := range syntax {
		if style.Fg == "" {
			continue
		}
		prio := 0
		if e, ok := defaultCaptureTable[scope]; ok {
			prio = e.priority
		}
		updated[scope] = captureEntry{ansi: syntaxANSI(style.Fg, style.Bold, style.Italic), priority: prio}
	}
	captureTable = updated
}

func syntaxANSI(fg string, bold, italic bool) string {
	color := hexToANSI(fg)
	var prefix string
	if bold {
		prefix += "\x1b[1m"
	}
	if italic {
		prefix += "\x1b[3m"
	}
	return prefix + color
}

func captureANSI(name string) (string, int, bool) {
	if e, ok := captureTable[name]; ok {
		return e.ansi, e.priority, true
	}
	// Prefix match: "keyword.special" → "keyword" at one lower priority.
	if idx := strings.Index(name, "."); idx >= 0 {
		prefix := name[:idx]
		if e, ok := captureTable[prefix]; ok {
			return e.ansi, e.priority - 1, true
		}
	}
	return "", 0, false
}

// --- custom queries for languages where nvim-treesitter queries use inheritance ---

const typescriptHighlightQuery = `
(comment) @comment

[
  (string)
  (template_string)
] @string

(number) @number

[
  "break" "case" "catch" "class" "const" "continue" "debugger" "default"
  "delete" "do" "else" "export" "extends" "finally" "for" "from" "function"
  "get" "if" "import" "in" "instanceof" "let" "new" "of" "return" "set"
  "static" "switch" "target" "throw" "try" "typeof" "var" "void" "while"
  "with" "yield" "async" "await" "implements" "interface" "private"
  "protected" "public" "readonly" "enum" "abstract" "declare" "namespace"
  "type" "override" "satisfies" "as" "module"
] @keyword

[
  (true)
  (false)
  (null)
  (undefined)
] @constant.builtin

(type_identifier) @type

(function_declaration name: (identifier) @function)
(method_definition name: (property_identifier) @function)
(function_signature name: (identifier) @function)
(method_signature name: (property_identifier) @function)

(call_expression function: (identifier) @function.call)
(call_expression function: (member_expression property: (property_identifier) @function.call))
(new_expression constructor: (identifier) @type)
`

const javascriptHighlightQuery = `
(comment) @comment

[
  (string)
  (template_string)
] @string

(number) @number

[
  "break" "case" "catch" "class" "const" "continue" "debugger" "default"
  "delete" "do" "else" "export" "extends" "finally" "for" "from" "function"
  "get" "if" "import" "in" "instanceof" "let" "new" "of" "return" "set"
  "static" "switch" "target" "throw" "try" "typeof" "var" "void" "while"
  "with" "yield" "async" "await"
] @keyword

[
  (true)
  (false)
  (null)
  (undefined)
] @constant.builtin

(function_declaration name: (identifier) @function)
(method_definition name: (property_identifier) @function)

(call_expression function: (identifier) @function.call)
(call_expression function: (member_expression property: (property_identifier) @function.call))
`

const cppHighlightQuery = `
(comment) @comment

[
  (string_literal)
  (raw_string_literal)
  (char_literal)
] @string

(number_literal) @number

[
  "break" "case" "catch" "class" "const" "constexpr" "consteval" "constinit"
  "continue" "co_await" "co_return" "co_yield" "default" "delete" "do" "else"
  "enum" "explicit" "export" "extern" "final" "for" "friend" "if" "inline"
  "mutable" "namespace" "new" "noexcept" "operator" "override" "private"
  "protected" "public" "register" "requires" "return" "sizeof" "static"
  "static_assert" "struct" "switch" "template" "throw" "try"
  "typedef" "typename" "union" "using" "virtual" "volatile" "while"
] @keyword

(true) @constant.builtin
(false) @constant.builtin

"nullptr" @constant.builtin

(type_identifier) @type

(function_declarator declarator: (identifier) @function)

(call_expression function: (identifier) @function.call)
(call_expression function: (field_expression field: (field_identifier) @function.call))
`

const htmlHighlightQuery = `
(comment) @comment

(quoted_attribute_value) @string

(attribute_name) @attribute

[
  (start_tag (tag_name) @tag)
  (end_tag (tag_name) @tag)
  (self_closing_tag (tag_name) @tag)
]
`

const phpHighlightQuery = `
(comment) @comment

[
  (string)
  (heredoc)
  (nowdoc)
] @string

(integer) @number
(float) @number

[
  "array" "break" "case" "catch" "class" "clone" "const"
  "continue" "declare" "default" "do" "echo" "else" "elseif"
  "enddeclare" "endfor" "endforeach" "endif" "endswitch" "endwhile"
  "enum" "extends" "finally" "fn" "for" "foreach" "function"
  "global" "goto" "if" "implements" "include" "include_once" "instanceof"
  "insteadof" "interface" "list" "match" "namespace" "new" "print"
  "private" "protected" "public" "readonly" "require" "require_once"
  "return" "static" "switch" "throw" "trait" "try" "unset" "use"
  "while" "yield" "abstract" "final"
] @keyword

(boolean) @constant.builtin
(null) @constant.builtin

(named_type (name) @type)

(function_definition name: (name) @function)
(method_declaration name: (name) @function)

(function_call_expression function: (name) @function.call)
(member_call_expression name: (name) @function.call)
`

const svelteHighlightQuery = `
(comment) @comment

(quoted_attribute_value) @string

(attribute_name) @attribute

[
  (start_tag (tag_name) @tag)
  (end_tag (tag_name) @tag)
  (self_closing_tag (tag_name) @tag)
]
`

const gdscriptHighlightQuery = `
(comment) @comment

[
  (string)
  (string_name)
] @string

(float) @number
(integer) @number

[
  (true)
  (false)
] @constant.builtin

(null) @constant

[
  "and" "as" "await" "break" "class" "const" "continue" "elif" "else"
  "enum" "extends" "for" "func" "if" "in" "is" "match" "not" "or"
  "pass" "return" "signal" "var" "while"
] @keyword

(function_definition (name) @function)
(constructor_definition "_init" @function)

(call (identifier) @function.call)
(attribute_call (identifier) @function.method.call)

(type (identifier) @type)
`
