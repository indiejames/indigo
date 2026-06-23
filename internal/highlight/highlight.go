package highlight

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/css"
	"github.com/smacker/go-tree-sitter/cue"
	"github.com/smacker/go-tree-sitter/dockerfile"
	"github.com/smacker/go-tree-sitter/elixir"
	"github.com/smacker/go-tree-sitter/elm"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/groovy"
	"github.com/smacker/go-tree-sitter/hcl"
	"github.com/smacker/go-tree-sitter/html"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/kotlin"
	"github.com/smacker/go-tree-sitter/lua"
	md "github.com/smacker/go-tree-sitter/markdown/tree-sitter-markdown"
	"github.com/smacker/go-tree-sitter/ocaml"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/protobuf"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/scala"
	"github.com/smacker/go-tree-sitter/sql"
	"github.com/smacker/go-tree-sitter/svelte"
	"github.com/smacker/go-tree-sitter/swift"
	"github.com/smacker/go-tree-sitter/toml"
	tsx "github.com/smacker/go-tree-sitter/typescript/tsx"
	typescript "github.com/smacker/go-tree-sitter/typescript/typescript"
	"github.com/smacker/go-tree-sitter/yaml"
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
	if lang == nil {
		return nil
	}
	q, err := sitter.NewQuery([]byte(qsrc), lang)
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
	tree, err := p.ParseCtx(context.Background(), nil, content)
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
	qc.Exec(query, tree.RootNode())
	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}
		m = qc.FilterPredicates(m, content)
		for _, c := range m.Captures {
			name := query.CaptureNameForId(c.Index)
			ansi, prio, ok := captureANSI(name)
			if !ok {
				continue
			}
			node := c.Node
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
	"comment":          {hexToANSI("#6A9955"), 100},
	"string":           {hexToANSI("#CE9178"), 90},
	"number":           {hexToANSI("#B5CEA8"), 80},
	"constant.builtin": {hexToANSI("#569CD6"), 75},
	"keyword":          {hexToANSI("#C586C0"), 70},
	"keyword.builtin":  {hexToANSI("#569CD6"), 70},
	"function":         {hexToANSI("#DCDCAA"), 60},
	"function.call":    {hexToANSI("#DCDCAA"), 55},
	"type":             {hexToANSI("#4EC9B0"), 50},
}

func captureANSI(name string) (string, int, bool) {
	if e, ok := captureTable[name]; ok {
		return e.ansi, e.priority, true
	}
	// Prefix match: "function.call" → "function" at one lower priority.
	if idx := strings.Index(name, "."); idx >= 0 {
		if e, ok := captureTable[name[:idx]]; ok {
			return e.ansi, e.priority - 1, true
		}
	}
	return "", 0, false
}

// --- language detection ---

func languageForPath(filePath string) (*sitter.Language, string) {
	// Check for extension-less filenames first.
	base := filePath
	if i := strings.LastIndex(filePath, "/"); i >= 0 {
		base = filePath[i+1:]
	}
	switch strings.ToLower(base) {
	case "dockerfile":
		return dockerfile.GetLanguage(), dockerfileHighlightQuery
	case "makefile", "gnumakefile":
		return nil, ""
	}

	ext := filePath
	if i := strings.LastIndex(filePath, "."); i >= 0 {
		ext = strings.ToLower(filePath[i:])
	}
	switch ext {
	case ".go":
		return golang.GetLanguage(), goHighlightQuery
	case ".ts":
		return typescript.GetLanguage(), typescriptHighlightQuery
	case ".tsx":
		return tsx.GetLanguage(), tsxHighlightQuery
	case ".js", ".mjs", ".cjs":
		return javascript.GetLanguage(), javascriptHighlightQuery
	case ".py":
		return python.GetLanguage(), pythonHighlightQuery
	case ".rs":
		return rust.GetLanguage(), rustHighlightQuery
	case ".c", ".h":
		return c.GetLanguage(), cHighlightQuery
	case ".cc", ".cpp", ".cxx", ".c++", ".hh", ".hpp", ".hxx":
		return cpp.GetLanguage(), cppHighlightQuery
	case ".java":
		return java.GetLanguage(), javaHighlightQuery
	case ".cs":
		return csharp.GetLanguage(), csharpHighlightQuery
	case ".rb":
		return ruby.GetLanguage(), rubyHighlightQuery
	case ".lua":
		return lua.GetLanguage(), luaHighlightQuery
	case ".swift":
		return swift.GetLanguage(), swiftHighlightQuery
	case ".kt", ".kts":
		return kotlin.GetLanguage(), kotlinHighlightQuery
	case ".scala", ".sc":
		return scala.GetLanguage(), scalaHighlightQuery
	case ".sh", ".bash":
		return bash.GetLanguage(), bashHighlightQuery
	case ".css":
		return css.GetLanguage(), cssHighlightQuery
	case ".html", ".htm":
		return html.GetLanguage(), htmlHighlightQuery
	case ".yaml", ".yml":
		return yaml.GetLanguage(), yamlHighlightQuery
	case ".toml":
		return toml.GetLanguage(), tomlHighlightQuery
	case ".json":
		return nil, ""
	case ".sql":
		return sql.GetLanguage(), sqlHighlightQuery
	case ".php":
		return php.GetLanguage(), phpHighlightQuery
	case ".proto":
		return protobuf.GetLanguage(), protobufHighlightQuery
	case ".ex", ".exs":
		return elixir.GetLanguage(), elixirHighlightQuery
	case ".elm":
		return elm.GetLanguage(), elmHighlightQuery
	case ".ml", ".mli":
		return ocaml.GetLanguage(), ocamlHighlightQuery
	case ".groovy", ".gvy", ".gy", ".gsh":
		return groovy.GetLanguage(), groovyHighlightQuery
	case ".tf", ".hcl":
		return hcl.GetLanguage(), hclHighlightQuery
	case ".svelte":
		return svelte.GetLanguage(), svelteHighlightQuery
	case ".cue":
		return cue.GetLanguage(), cueHighlightQuery
	case ".md", ".markdown":
		return md.GetLanguage(), markdownHighlightQuery
	default:
		return nil, ""
	}
}

// --- highlight queries ---

const goHighlightQuery = `
(comment) @comment

[
  (interpreted_string_literal)
  (raw_string_literal)
  (rune_literal)
] @string

[
  (int_literal)
  (float_literal)
  (imaginary_literal)
] @number

[
  "break" "case" "chan" "const" "continue" "default"
  "defer" "else" "fallthrough" "for" "func" "go"
  "goto" "if" "import" "interface" "map" "package"
  "range" "return" "select" "struct" "switch" "type"
  "var"
] @keyword

[
  (true)
  (false)
  (nil)
] @constant.builtin

(type_identifier) @type

(function_declaration name: (identifier) @function)
(method_declaration name: (field_identifier) @function)

(call_expression function: (identifier) @function.call)
(call_expression function: (selector_expression field: (field_identifier) @function.call))
`

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
(abstract_method_signature name: (property_identifier) @function)

(call_expression function: (identifier) @function.call)
(call_expression function: (member_expression property: (property_identifier) @function.call))
(new_expression constructor: (identifier) @type)
`

const tsxHighlightQuery = typescriptHighlightQuery

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

const pythonHighlightQuery = `
(comment) @comment

(string) @string

[
  (integer)
  (float)
  (complex)
] @number

[
  "and" "as" "assert" "async" "await" "break" "class" "continue" "def"
  "del" "elif" "else" "except" "finally" "for" "from" "global" "if"
  "import" "in" "is" "lambda" "nonlocal" "not" "or" "pass" "raise"
  "return" "try" "while" "with" "yield"
] @keyword

[
  (true)
  (false)
  (none)
] @constant.builtin

(function_definition name: (identifier) @function)

(call function: (identifier) @function.call)
(call function: (attribute attribute: (identifier) @function.call))
`

const rustHighlightQuery = `
[
  (line_comment)
  (block_comment)
] @comment

[
  (string_literal)
  (raw_string_literal)
  (char_literal)
] @string

[
  (integer_literal)
  (float_literal)
] @number

[
  "as" "async" "await" "break" "const" "continue" "crate" "dyn" "else"
  "enum" "extern" "fn" "for" "if" "impl" "in" "let" "loop" "match" "mod"
  "move" "mut" "pub" "ref" "return" "self" "Self" "static" "struct"
  "super" "trait" "type" "unsafe" "use" "where" "while"
] @keyword

(boolean_literal) @constant.builtin

(type_identifier) @type

(function_item name: (identifier) @function)

(call_expression function: (identifier) @function.call)
(call_expression function: (field_expression field: (field_identifier) @function.call))
(call_expression function: (scoped_identifier name: (identifier) @function.call))
`

const cHighlightQuery = `
(comment) @comment

[
  (string_literal)
  (char_literal)
] @string

(number_literal) @number

[
  "break" "case" "const" "continue" "default" "do" "else" "enum" "extern"
  "for" "if" "inline" "register" "return" "sizeof" "static" "struct"
  "switch" "typedef" "union" "volatile" "while"
] @keyword

[
  "NULL" "nullptr"
] @constant.builtin

(type_identifier) @type

(function_declarator declarator: (identifier) @function)

(call_expression function: (identifier) @function.call)
(call_expression function: (field_expression field: (field_identifier) @function.call))
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
  "static_assert" "struct" "switch" "template" "this" "throw" "try"
  "typedef" "typename" "union" "using" "virtual" "volatile" "while"
] @keyword

[
  "true" "false" "nullptr" "NULL"
] @constant.builtin

(type_identifier) @type

(function_declarator declarator: (identifier) @function)

(call_expression function: (identifier) @function.call)
(call_expression function: (field_expression field: (field_identifier) @function.call))
`

const javaHighlightQuery = `
[
  (line_comment)
  (block_comment)
] @comment

[
  (string_literal)
  (text_block)
] @string

[
  (decimal_integer_literal)
  (hex_integer_literal)
  (octal_integer_literal)
  (binary_integer_literal)
  (decimal_floating_point_literal)
  (hex_floating_point_literal)
] @number

[
  "abstract" "assert" "break" "case" "catch" "class" "continue" "default"
  "do" "else" "enum" "extends" "final" "finally" "for" "if" "implements"
  "import" "instanceof" "interface" "new" "package" "private" "protected"
  "public" "return" "sealed" "static" "strictfp" "super" "switch"
  "synchronized" "this" "throw" "throws" "transient" "try" "volatile" "while"
] @keyword

[
  (true)
  (false)
  (null_literal)
] @constant.builtin

(type_identifier) @type

(method_declaration name: (identifier) @function)

(method_invocation name: (identifier) @function.call)
`

const csharpHighlightQuery = `
[
  (comment)
  (multiline_comment)
] @comment

[
  (string_literal)
  (verbatim_string_literal)
  (interpolated_string_expression)
] @string

[
  (integer_literal)
  (real_literal)
] @number

[
  "abstract" "as" "async" "await" "break" "case" "catch" "checked" "class"
  "const" "continue" "default" "delegate" "do" "else" "enum" "event"
  "explicit" "extern" "finally" "fixed" "for" "foreach" "goto" "if"
  "implicit" "in" "interface" "internal" "is" "lock" "namespace" "new"
  "operator" "out" "override" "params" "private" "protected" "public"
  "readonly" "ref" "return" "sealed" "sizeof" "stackalloc" "static"
  "struct" "switch" "this" "throw" "try" "typeof" "unchecked" "unsafe"
  "using" "var" "virtual" "void" "volatile" "while" "yield" "record"
  "with" "init" "required"
] @keyword

[
  (true)
  (false)
  (null_literal)
] @constant.builtin

(method_declaration name: (identifier) @function)

(invocation_expression function: (identifier) @function.call)
(invocation_expression function: (member_access_expression name: (identifier) @function.call))
`

const rubyHighlightQuery = `
(comment) @comment

[
  (string)
  (heredoc_body)
] @string

[
  (integer)
  (float)
] @number

[
  "begin" "class" "def" "do" "else" "elsif" "end" "ensure" "for" "if"
  "in" "module" "next" "raise" "rescue" "return" "self" "super" "then"
  "unless" "until" "when" "while" "yield"
] @keyword

[
  (true)
  (false)
  (nil)
] @constant.builtin

(constant) @type

(method name: (identifier) @function)

(call method: (identifier) @function.call)
`

const luaHighlightQuery = `
(comment) @comment

(string) @string

(number) @number

[
  "and" "break" "do" "else" "elseif" "end" "for" "function" "goto" "if"
  "in" "local" "not" "or" "repeat" "return" "then" "until" "while"
] @keyword

[
  "true" "false" "nil"
] @constant.builtin

(function_declaration name: (identifier) @function)

(function_call name: (identifier) @function.call)
`

const swiftHighlightQuery = `
[
  (comment)
  (multiline_comment)
] @comment

[
  (line_string_literal)
  (multi_line_string_literal)
] @string

[
  (integer_literal)
  (hex_literal)
  (oct_literal)
  (bin_literal)
  (real_literal)
] @number

[
  "as" "async" "await" "break" "case" "catch" "class" "continue"
  "convenience" "default" "defer" "deinit" "do" "dynamic" "else" "enum"
  "extension" "fallthrough" "fileprivate" "final" "for" "func" "get"
  "guard" "if" "import" "in" "indirect" "init" "inout" "internal" "is"
  "lazy" "let" "mutating" "nonmutating" "open" "operator" "optional"
  "override" "package" "private" "protocol" "public" "repeat" "required"
  "rethrows" "return" "self" "set" "some" "static" "struct" "subscript"
  "super" "switch" "throw" "throws" "try" "typealias" "unowned" "var"
  "weak" "where" "while"
] @keyword

[
  "true" "false" "nil"
] @constant.builtin

(type_identifier) @type

(function_declaration name: (simple_identifier) @function)

(call_expression function: (simple_identifier) @function.call)
`

const kotlinHighlightQuery = `
[
  (multiline_comment)
  (line_comment)
] @comment

[
  (line_string_literal)
  (multi_line_string_literal)
] @string

[
  (integer_literal)
  (long_literal)
  (real_literal)
  (hex_literal)
  (bin_literal)
] @number

[
  "abstract" "actual" "annotation" "as" "break" "by" "catch" "class"
  "companion" "const" "constructor" "continue" "crossinline" "data" "do"
  "else" "enum" "expect" "external" "final" "finally" "for" "fun" "get"
  "if" "import" "in" "infix" "init" "inline" "inner" "interface" "internal"
  "is" "lateinit" "noinline" "object" "open" "operator" "out" "override"
  "package" "private" "protected" "public" "reified" "return" "sealed"
  "set" "suspend" "tailrec" "throw" "try" "typealias" "val" "var" "vararg"
  "when" "where" "while"
] @keyword

[
  "true" "false" "null"
] @constant.builtin

(type_identifier) @type

(function_declaration (simple_identifier) @function)

(call_expression (simple_identifier) @function.call)
`

const scalaHighlightQuery = `
[
  (comment)
  (block_comment)
] @comment

[
  (string)
  (interpolated_string_expression)
] @string

[
  (integer_literal)
  (floating_point_literal)
] @number

[
  "abstract" "case" "catch" "class" "def" "do" "else" "extends" "final"
  "finally" "for" "forSome" "if" "implicit" "import" "lazy" "match" "new"
  "object" "override" "package" "private" "protected" "return" "sealed"
  "super" "this" "throw" "trait" "try" "type" "val" "var" "while" "with"
  "yield" "given" "using" "enum" "export"
] @keyword

[
  (true)
  (false)
  (null_literal)
] @constant.builtin

(type_identifier) @type

(function_definition name: (identifier) @function)

(call_expression function: (identifier) @function.call)
(call_expression function: (field_expression field: (identifier) @function.call))
`

const bashHighlightQuery = `
(comment) @comment

[
  (string)
  (raw_string)
  (ansi_c_string)
] @string

[
  "case" "do" "done" "elif" "else" "esac" "fi" "for" "function"
  "if" "in" "select" "then" "until" "while"
] @keyword

(function_definition name: (word) @function)

(command name: (word) @function.call)
`

const cssHighlightQuery = `
(comment) @comment

(string_value) @string

[
  (integer_value)
  (float_value)
] @number

(property_name) @function

(tag_name) @keyword.builtin

[
  "@media" "@import" "@keyframes" "@charset" "@namespace" "@supports"
  "@layer" "@font-face" "@page" "@counter-style"
] @keyword
`

const htmlHighlightQuery = `
(comment) @comment

(quoted_attribute_value) @string

(attribute_name) @function

[
  (start_tag (tag_name) @keyword.builtin)
  (end_tag (tag_name) @keyword.builtin)
  (self_closing_tag (tag_name) @keyword.builtin)
]
`

const yamlHighlightQuery = `
(comment) @comment

[
  (double_quote_scalar)
  (single_quote_scalar)
  (block_scalar)
  (flow_scalar)
] @string

(boolean_scalar) @constant.builtin

(null_scalar) @constant.builtin

(integer_scalar) @number
(float_scalar) @number
`

const tomlHighlightQuery = `
(comment) @comment

[
  (basic_string)
  (literal_string)
  (multiline_basic_string)
  (multiline_literal_string)
] @string

[
  (integer)
  (float)
] @number

(boolean) @constant.builtin

(bare_key) @function

(table (key (bare_key) @type))
`

const sqlHighlightQuery = `
[
  (comment)
  (marginalia)
] @comment

(literal) @string

(number) @number

[
  "select" "from" "where" "and" "or" "not" "insert" "into" "values"
  "update" "set" "delete" "create" "table" "alter" "drop" "index"
  "unique" "primary" "key" "foreign" "references" "on" "join" "left"
  "right" "inner" "outer" "full" "cross" "group" "by" "order" "having"
  "limit" "offset" "as" "distinct" "all" "null" "is" "in" "like"
  "between" "exists" "case" "when" "then" "else" "end" "union" "with"
  "returning" "if" "begin" "commit" "rollback" "transaction" "using"
  "natural" "asc" "desc"
] @keyword
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
  "array" "break" "callable" "case" "catch" "class" "clone" "const"
  "continue" "declare" "default" "do" "echo" "else" "elseif" "empty"
  "enddeclare" "endfor" "endforeach" "endif" "endswitch" "endwhile"
  "enum" "eval" "extends" "finally" "fn" "for" "foreach" "function"
  "global" "goto" "if" "implements" "include" "include_once" "instanceof"
  "insteadof" "interface" "isset" "list" "match" "namespace" "new" "print"
  "private" "protected" "public" "readonly" "require" "require_once"
  "return" "self" "static" "switch" "throw" "trait" "try" "unset" "use"
  "var" "while" "yield" "abstract" "final"
] @keyword

[
  (true)
  (false)
  (null)
] @constant.builtin

(named_type (name) @type)

(function_definition name: (name) @function)
(method_declaration name: (name) @function)

(function_call_expression function: (name) @function.call)
(member_call_expression name: (name) @function.call)
`

const protobufHighlightQuery = `
(comment) @comment

(string) @string

[
  (int_lit)
  (float_lit)
] @number

[
  "message" "service" "rpc" "returns" "syntax" "import" "package"
  "option" "enum" "oneof" "map" "repeated" "optional" "required"
  "reserved" "extend" "extensions" "weak" "public" "to" "stream"
] @keyword

(message_name) @type
(enum_name) @type
(service_name) @type

(rpc_name) @function
`

const elixirHighlightQuery = `
(comment) @comment

[
  (string)
  (charlist)
  (heredoc)
] @string

[
  (integer)
  (float)
] @number

[
  "after" "and" "catch" "do" "else" "end" "fn" "for" "if" "in"
  "not" "or" "rescue" "try" "unless" "when" "with" "cond" "case"
  "receive" "quote" "unquote"
] @keyword

[
  (true)
  (false)
  (nil)
] @constant.builtin

(atom) @string

(call target: (identifier) @function.call)
`

const elmHighlightQuery = `
(line_comment) @comment

(block_comment) @comment

[
  (string_constant_expr)
  (character_constant_expr)
] @string

(number_constant_expr) @number

[
  "if" "then" "else" "case" "of" "let" "in" "where" "type"
  "alias" "port" "module" "exposing" "import" "as"
] @keyword

(upper_case_qid (upper_case_identifier) @type)

(function_declaration_left (lower_case_identifier) @function)
`

const ocamlHighlightQuery = `
[
  (comment)
  (doc_comment)
] @comment

[
  (string)
  (character_literal)
  (quoted_string)
] @string

(number) @number

[
  "and" "as" "assert" "begin" "class" "constraint" "do" "done" "downto"
  "else" "end" "exception" "external" "for" "fun" "function" "functor"
  "if" "in" "include" "inherit" "initializer" "lazy" "let" "match"
  "method" "module" "mutable" "new" "nonrec" "object" "of" "open" "or"
  "private" "rec" "sig" "struct" "then" "to" "try" "type" "val"
  "virtual" "when" "while" "with"
] @keyword

[
  "true" "false"
] @constant.builtin

(type_constructor_path (type_constructor) @type)

(let_binding pattern: (value_name) @function)

(application_expression function: (value_path (value_name) @function.call))
`

const groovyHighlightQuery = `
(comment) @comment

[
  (string_literal)
  (gstring)
] @string

[
  (integer_literal)
  (floating_point_literal)
] @number

[
  "abstract" "as" "assert" "break" "case" "catch" "class" "continue"
  "def" "default" "do" "else" "enum" "extends" "final" "finally" "for"
  "if" "implements" "import" "in" "instanceof" "interface" "new" "package"
  "private" "protected" "public" "return" "static" "super" "switch"
  "this" "throw" "throws" "trait" "try" "while"
] @keyword

[
  (true)
  (false)
  (null)
] @constant.builtin

(class_declaration name: (identifier) @type)

(method_declaration name: (identifier) @function)

(method_call (identifier) @function.call)
`

const hclHighlightQuery = `
(comment) @comment

[
  (template_literal)
  (quoted_template)
  (heredoc_template)
] @string

(numeric_lit) @number

[
  "true" "false" "null"
] @constant.builtin

(function_call (identifier) @function.call)

(block (identifier) @keyword)
`

const svelteHighlightQuery = `
(comment) @comment
(script_element (raw_text) @string)
`

const cueHighlightQuery = `
(comment) @comment

[
  (string_lit)
  (bytes_lit)
  (multiline_string_lit)
  (multiline_bytes_lit)
] @string

(number_lit) @number

[
  "package" "import" "let" "if" "for" "in" "null" "true" "false"
] @keyword

[
  (true)
  (false)
  (null)
] @constant.builtin
`

const markdownHighlightQuery = `
(atx_heading) @keyword

(setext_heading) @keyword

(fenced_code_block) @string

(code_span) @string

(link_destination) @function.call

(image) @function
`

const dockerfileHighlightQuery = `
(comment) @comment

[
  (double_quoted_string)
  (single_quoted_string)
  (unquoted_string)
] @string

[
  "FROM" "RUN" "CMD" "LABEL" "EXPOSE" "ENV" "ADD" "COPY"
  "ENTRYPOINT" "VOLUME" "USER" "WORKDIR" "ARG" "ONBUILD"
  "STOPSIGNAL" "HEALTHCHECK" "SHELL"
] @keyword
`
