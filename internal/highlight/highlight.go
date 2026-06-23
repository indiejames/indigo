package highlight

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/asm"
	"github.com/alexaandru/go-sitter-forest/bash"
	c_lang "github.com/alexaandru/go-sitter-forest/c"
	"github.com/alexaandru/go-sitter-forest/c_sharp"
	"github.com/alexaandru/go-sitter-forest/clojure"
	"github.com/alexaandru/go-sitter-forest/cpp"
	"github.com/alexaandru/go-sitter-forest/css"
	"github.com/alexaandru/go-sitter-forest/cue"
	"github.com/alexaandru/go-sitter-forest/dart"
	"github.com/alexaandru/go-sitter-forest/dockerfile"
	"github.com/alexaandru/go-sitter-forest/elixir"
	"github.com/alexaandru/go-sitter-forest/elm"
	"github.com/alexaandru/go-sitter-forest/erlang"
	"github.com/alexaandru/go-sitter-forest/gdscript"
	"github.com/alexaandru/go-sitter-forest/gleam"
	golang_lang "github.com/alexaandru/go-sitter-forest/go"
	"github.com/alexaandru/go-sitter-forest/graphql"
	"github.com/alexaandru/go-sitter-forest/groovy"
	"github.com/alexaandru/go-sitter-forest/haskell"
	"github.com/alexaandru/go-sitter-forest/hcl"
	"github.com/alexaandru/go-sitter-forest/html"
	"github.com/alexaandru/go-sitter-forest/java"
	"github.com/alexaandru/go-sitter-forest/javascript"
	json_lang "github.com/alexaandru/go-sitter-forest/json"
	"github.com/alexaandru/go-sitter-forest/julia"
	"github.com/alexaandru/go-sitter-forest/kotlin"
	"github.com/alexaandru/go-sitter-forest/lua"
	"github.com/alexaandru/go-sitter-forest/markdown"
	"github.com/alexaandru/go-sitter-forest/nim"
	"github.com/alexaandru/go-sitter-forest/nix"
	"github.com/alexaandru/go-sitter-forest/ocaml"
	"github.com/alexaandru/go-sitter-forest/php"
	"github.com/alexaandru/go-sitter-forest/proto"
	"github.com/alexaandru/go-sitter-forest/python"
	r_lang "github.com/alexaandru/go-sitter-forest/r"
	"github.com/alexaandru/go-sitter-forest/ruby"
	"github.com/alexaandru/go-sitter-forest/rust"
	"github.com/alexaandru/go-sitter-forest/scala"
	"github.com/alexaandru/go-sitter-forest/sql"
	"github.com/alexaandru/go-sitter-forest/svelte"
	"github.com/alexaandru/go-sitter-forest/swift"
	"github.com/alexaandru/go-sitter-forest/toml"
	"github.com/alexaandru/go-sitter-forest/tsx"
	"github.com/alexaandru/go-sitter-forest/typescript"
	"github.com/alexaandru/go-sitter-forest/yaml"
	"github.com/alexaandru/go-sitter-forest/zig"
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
	return &Highlighter{lang: lang, query: q}
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
	return extractSpans(h.query, tree, content)
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

type captureEntry struct {
	ansi     string
	priority int
}

var captureTable = map[string]captureEntry{
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
	"punctuation.bracket":   {hexToANSI("#D4D4D4"), 20},
	"punctuation.delimiter": {hexToANSI("#D4D4D4"), 20},
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

// --- language detection ---

func languageForPath(filePath string) (*sitter.Language, []byte) {
	// Check for extension-less filenames first.
	base := filePath
	if i := strings.LastIndex(filePath, "/"); i >= 0 {
		base = filePath[i+1:]
	}
	switch strings.ToLower(base) {
	case "dockerfile":
		return sitter.NewLanguage(dockerfile.GetLanguage()), dockerfile.GetQuery("highlights")
	case "makefile", "gnumakefile":
		return nil, nil
	}

	ext := filePath
	if i := strings.LastIndex(filePath, "."); i >= 0 {
		ext = strings.ToLower(filePath[i:])
	}
	switch ext {
	case ".go":
		return sitter.NewLanguage(golang_lang.GetLanguage()), golang_lang.GetQuery("highlights")
	case ".ts":
		return sitter.NewLanguage(typescript.GetLanguage()), []byte(typescriptHighlightQuery)
	case ".tsx":
		return sitter.NewLanguage(tsx.GetLanguage()), []byte(typescriptHighlightQuery)
	case ".js", ".mjs", ".cjs":
		return sitter.NewLanguage(javascript.GetLanguage()), []byte(javascriptHighlightQuery)
	case ".py":
		return sitter.NewLanguage(python.GetLanguage()), python.GetQuery("highlights")
	case ".rs":
		return sitter.NewLanguage(rust.GetLanguage()), rust.GetQuery("highlights")
	case ".c", ".h":
		return sitter.NewLanguage(c_lang.GetLanguage()), c_lang.GetQuery("highlights")
	case ".cc", ".cpp", ".cxx", ".c++", ".hh", ".hpp", ".hxx":
		return sitter.NewLanguage(cpp.GetLanguage()), []byte(cppHighlightQuery)
	case ".java":
		return sitter.NewLanguage(java.GetLanguage()), java.GetQuery("highlights")
	case ".cs":
		return sitter.NewLanguage(c_sharp.GetLanguage()), c_sharp.GetQuery("highlights")
	case ".rb":
		return sitter.NewLanguage(ruby.GetLanguage()), ruby.GetQuery("highlights")
	case ".lua":
		return sitter.NewLanguage(lua.GetLanguage()), lua.GetQuery("highlights")
	case ".swift":
		return sitter.NewLanguage(swift.GetLanguage()), swift.GetQuery("highlights")
	case ".kt", ".kts":
		return sitter.NewLanguage(kotlin.GetLanguage()), kotlin.GetQuery("highlights")
	case ".scala", ".sc":
		return sitter.NewLanguage(scala.GetLanguage()), scala.GetQuery("highlights")
	case ".sh", ".bash":
		return sitter.NewLanguage(bash.GetLanguage()), bash.GetQuery("highlights")
	case ".css":
		return sitter.NewLanguage(css.GetLanguage()), css.GetQuery("highlights")
	case ".html", ".htm":
		return sitter.NewLanguage(html.GetLanguage()), []byte(htmlHighlightQuery)
	case ".yaml", ".yml":
		return sitter.NewLanguage(yaml.GetLanguage()), yaml.GetQuery("highlights")
	case ".toml":
		return sitter.NewLanguage(toml.GetLanguage()), toml.GetQuery("highlights")
	case ".json":
		return sitter.NewLanguage(json_lang.GetLanguage()), json_lang.GetQuery("highlights")
	case ".sql":
		return sitter.NewLanguage(sql.GetLanguage()), sql.GetQuery("highlights")
	case ".php":
		return sitter.NewLanguage(php.GetLanguage()), []byte(phpHighlightQuery)
	case ".proto":
		return sitter.NewLanguage(proto.GetLanguage()), proto.GetQuery("highlights")
	case ".ex", ".exs":
		return sitter.NewLanguage(elixir.GetLanguage()), elixir.GetQuery("highlights")
	case ".elm":
		return sitter.NewLanguage(elm.GetLanguage()), elm.GetQuery("highlights")
	case ".ml", ".mli":
		return sitter.NewLanguage(ocaml.GetLanguage()), ocaml.GetQuery("highlights")
	case ".groovy", ".gvy", ".gy", ".gsh":
		return sitter.NewLanguage(groovy.GetLanguage()), groovy.GetQuery("highlights")
	case ".tf", ".hcl":
		return sitter.NewLanguage(hcl.GetLanguage()), hcl.GetQuery("highlights")
	case ".svelte":
		return sitter.NewLanguage(svelte.GetLanguage()), []byte(svelteHighlightQuery)
	case ".cue":
		return sitter.NewLanguage(cue.GetLanguage()), cue.GetQuery("highlights")
	case ".md", ".markdown":
		return sitter.NewLanguage(markdown.GetLanguage()), markdown.GetQuery("highlights")
	case ".gd":
		return sitter.NewLanguage(gdscript.GetLanguage()), []byte(gdscriptHighlightQuery)
	case ".zig":
		return sitter.NewLanguage(zig.GetLanguage()), zig.GetQuery("highlights")
	case ".dart":
		return sitter.NewLanguage(dart.GetLanguage()), dart.GetQuery("highlights")
	case ".nim":
		return sitter.NewLanguage(nim.GetLanguage()), nim.GetQuery("highlights")
	case ".gleam":
		return sitter.NewLanguage(gleam.GetLanguage()), gleam.GetQuery("highlights")
	case ".hs", ".lhs":
		return sitter.NewLanguage(haskell.GetLanguage()), haskell.GetQuery("highlights")
	case ".erl", ".hrl":
		return sitter.NewLanguage(erlang.GetLanguage()), erlang.GetQuery("highlights")
	case ".clj", ".cljs", ".cljc", ".edn":
		return sitter.NewLanguage(clojure.GetLanguage()), clojure.GetQuery("highlights")
	case ".graphql", ".gql":
		return sitter.NewLanguage(graphql.GetLanguage()), graphql.GetQuery("highlights")
	case ".nix":
		return sitter.NewLanguage(nix.GetLanguage()), nix.GetQuery("highlights")
	case ".r":
		return sitter.NewLanguage(r_lang.GetLanguage()), r_lang.GetQuery("highlights")
	case ".jl":
		return sitter.NewLanguage(julia.GetLanguage()), julia.GetQuery("highlights")
	case ".s", ".asm":
		return sitter.NewLanguage(asm.GetLanguage()), asm.GetQuery("highlights")
	default:
		return nil, nil
	}
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
