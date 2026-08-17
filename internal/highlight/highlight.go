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
	lang        *sitter.Language
	query       *sitter.Query
	extra       *Highlighter // optional secondary parser (e.g. markdown_inline)
	indentQuery *sitter.Query
	// postProcess computes additional spans by inspecting content directly
	// rather than through h.query — for constructs a language's grammar
	// doesn't expose as a syntax node at all (e.g. Rust's `{name}` captured
	// identifiers inside println!/format!-family macro strings, which
	// tree-sitter-rust parses as opaque string_content with no substructure;
	// see lang_rust.go). Its spans are prepended, not appended, ahead of
	// h.query's own spans on the same line, so they win over an enclosing
	// capture (e.g. @string) at any column they cover regardless of that
	// capture's table priority — see captureANSI.
	postProcess func([]byte) LineSpans
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
	if iqsrc := indentQueryForPath(filePath); len(iqsrc) > 0 {
		if iq, err := sitter.NewQuery(lang, iqsrc); err == nil {
			h.indentQuery = iq
		}
	}
	h.postProcess = postProcessForPath(filePath)
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
	if h.postProcess != nil {
		for line, spans := range h.postProcess(content) {
			result[line] = append(spans, result[line]...)
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

// --- tree-sitter text objects ---

// TextObject describes a source region found by a text-object query.
type TextObject struct {
	StartLine, StartCol int
	EndLine, EndCol     int
}

// node type sets shared across languages.
var (
	tsFunctionTypes = map[string]bool{
		"function_declaration": true, "method_declaration": true, "func_literal": true,
		"function_definition": true, "function_item": true,
		"arrow_function": true, "function": true, "method_definition": true,
		"constructor_declaration": true,
	}
	tsTypeTypes = map[string]bool{
		"type_declaration": true, "type_spec": true,
		"struct_item": true, "enum_item": true, "type_item": true, "impl_item": true,
		"interface_declaration": true, "class_declaration": true, "type_alias_declaration": true,
		"class_definition": true, "struct_specifier": true, "enum_specifier": true,
	}
	tsBlockTypes = map[string]bool{
		"block": true, "statement_block": true,
		"declaration_list": true, "field_declaration_list": true,
	}
	tsCommentTypes = map[string]bool{
		"comment": true, "line_comment": true, "block_comment": true, "doc_comment": true,
	}
	tsArgListTypes = map[string]bool{
		"argument_list": true, "arguments": true,
		"call_arguments": true, "positional_arguments": true,
	}
	// tsInlineBraceTypes are node types whose braces wrap a construct that's
	// conventionally written on one line rather than expanded into a block:
	// import/export specifier lists, destructuring patterns, and object
	// literals. Used to decide whether typing '{' should expand into an
	// indented block or just insert "{}".
	//
	// Currently JS/TS/TSX-specific: these are the exact node type names from
	// those three grammars (verified against their parser.c symbol tables).
	// Other brace languages (Go composite literals, Python dict/set
	// literals, Rust struct expressions, ...) have analogous "written
	// inline" constructs but aren't covered here yet — ShouldExpandBraceBlock
	// falls back to always expanding for them, matching the pre-existing
	// behavior for every language before this feature existed. Extending
	// this map to another language needs the same node-type verification,
	// not just a name guess.
	tsInlineBraceTypes = map[string]bool{
		"named_imports":  true,
		"export_clause":  true,
		"object_pattern": true,
		"object":         true,
		"jsx_expression": true,
	}
)

// TextObjectAt finds the "inside" text object of the given kind at (line, col).
// kind is one of: "function", "type", "argument", "comment".
// Returns (obj, true) on success.
func (h *Highlighter) TextObjectAt(content []byte, line, col int, kind string) (TextObject, bool) {
	if h == nil {
		return TextObject{}, false
	}
	lines := strings.Split(string(content), "\n")
	if line < 0 || line >= len(lines) {
		return TextObject{}, false
	}
	p := sitter.NewParser()
	p.SetLanguage(h.lang)
	tree, err := p.ParseString(context.Background(), nil, content)
	if err != nil || tree == nil {
		return TextObject{}, false
	}
	defer tree.Close()

	// col is rune-based (matching the client's cursor column); tree-sitter
	// Points use byte columns.
	byteCol := runeColToByteCol(lines[line], col)
	pt := sitter.Point{Row: uint(line), Column: uint(byteCol)}
	leaf := tree.RootNode().DescendantForPointRange(pt, pt)
	if leaf.IsNull() {
		return TextObject{}, false
	}

	switch kind {
	case "function":
		return tsFunctionInside(leaf, lines)
	case "type":
		return tsTypeInside(leaf, lines)
	case "argument":
		return tsArgumentAt(leaf, pt, lines)
	case "comment":
		return tsCommentAt(leaf, lines)
	}
	return TextObject{}, false
}

// TextObjectAround finds the full "around" span of the given kind at (line, col).
// Unlike TextObjectAt, it returns the complete node span including delimiters.
// kind is one of: "function", "type", "argument", "comment".
func (h *Highlighter) TextObjectAround(content []byte, line, col int, kind string) (TextObject, bool) {
	if h == nil {
		return TextObject{}, false
	}
	lines := strings.Split(string(content), "\n")
	if line < 0 || line >= len(lines) {
		return TextObject{}, false
	}
	p := sitter.NewParser()
	p.SetLanguage(h.lang)
	tree, err := p.ParseString(context.Background(), nil, content)
	if err != nil || tree == nil {
		return TextObject{}, false
	}
	defer tree.Close()

	// col is rune-based (matching the client's cursor column); tree-sitter
	// Points use byte columns.
	byteCol := runeColToByteCol(lines[line], col)
	pt := sitter.Point{Row: uint(line), Column: uint(byteCol)}
	leaf := tree.RootNode().DescendantForPointRange(pt, pt)
	if leaf.IsNull() {
		return TextObject{}, false
	}

	switch kind {
	case "function":
		fn, ok := tsAncestor(leaf, tsFunctionTypes)
		if !ok {
			return TextObject{}, false
		}
		return tsNodeSpan(fn, lines), true
	case "type":
		tn, ok := tsAncestor(leaf, tsTypeTypes)
		if !ok {
			return TextObject{}, false
		}
		// For Go: the top-level type_declaration is what we want.
		// Walk up one more level if the direct ancestor is a nested spec.
		parent := tn.Parent()
		if !parent.IsNull() && tsTypeTypes[parent.Type()] {
			return tsNodeSpan(parent, lines), true
		}
		return tsNodeSpan(tn, lines), true
	case "argument":
		return tsArgumentAt(leaf, pt, lines)
	case "comment":
		return tsCommentAt(leaf, lines)
	}
	return TextObject{}, false
}

// IsInString reports whether (line, col) falls inside a string/template
// literal, per the language's syntax tree — i.e. any node captured
// "string" (or a "string.*" subtype, such as a template literal) by the
// language's highlight query spans that position.
func (h *Highlighter) IsInString(content []byte, line, col int) bool {
	if h == nil {
		return false
	}

	lines := strings.Split(string(content), "\n")
	if line < 0 || line >= len(lines) {
		return false
	}
	byteCol := runeColToByteCol(lines[line], col)

	p := sitter.NewParser()
	p.SetLanguage(h.lang)
	tree, err := p.ParseString(context.Background(), nil, content)
	if err != nil || tree == nil {
		return false
	}
	defer tree.Close()

	pt := sitter.Point{Row: uint(line), Column: uint(byteCol)}
	qc := sitter.NewQueryCursor()
	matches := qc.Matches(h.query, tree.RootNode(), content)
	var spans []sitter.Range
	for {
		m := matches.Next()
		if m == nil {
			break
		}
		for _, cap := range m.Captures {
			name := h.query.CaptureNameForID(cap.Index)
			if name != "string" && !strings.HasPrefix(name, "string.") {
				continue
			}
			spans = append(spans, sitter.Range{StartPoint: cap.Node.StartPoint(), EndPoint: cap.Node.EndPoint()})
		}
	}
	for _, r := range mergeAdjacentRanges(spans) {
		if pointWithin(pt, r.StartPoint, r.EndPoint) {
			return true
		}
	}
	return false
}

// mergeAdjacentRanges coalesces touching or overlapping ranges (sorted by
// start point) into their union. Some languages' highlight queries capture
// a single string/template literal as several sibling sub-spans rather than
// one contiguous node — e.g. query_ecma.go's template_string pattern
// captures each backtick delimiter and string_fragment separately (not the
// whole node) so a ${...} interpolation's contents can outrank the string
// color. IsInString's pointWithin check is deliberately exclusive of both
// endpoints so a cursor just outside a string's outer delimiters reads as
// "outside" — but applied to the unmerged sub-spans, that same exclusivity
// turns the seam between two adjacent sub-spans (e.g. the boundary between
// a string_fragment and the backtick right after it) into a false gap: a
// cursor sitting exactly on that seam is still inside the literal, but
// belongs to neither sub-span's interior. Merging first restores one
// contiguous range per literal, so only its true outer boundary excludes.
func mergeAdjacentRanges(ranges []sitter.Range) []sitter.Range {
	if len(ranges) == 0 {
		return nil
	}
	sort.Slice(ranges, func(i, j int) bool {
		return pointLess(ranges[i].StartPoint, ranges[j].StartPoint)
	})
	merged := []sitter.Range{ranges[0]}
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		if pointLess(r.StartPoint, last.EndPoint) || r.StartPoint == last.EndPoint {
			if pointLess(last.EndPoint, r.EndPoint) {
				last.EndPoint = r.EndPoint
			}
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

// pointLess reports whether a comes strictly before b in (row, column)
// order.
func pointLess(a, b sitter.Point) bool {
	if a.Row != b.Row {
		return a.Row < b.Row
	}
	return a.Column < b.Column
}

// pointWithin reports whether pt (a cursor position — the insertion point
// immediately before the rune at pt.Column) falls strictly between start and
// end, exclusive of both endpoints: a cursor sitting immediately before the
// opening delimiter or immediately after the closing one counts as outside.
func pointWithin(pt, start, end sitter.Point) bool {
	if pt.Row < start.Row || pt.Row > end.Row {
		return false
	}
	if pt.Row == start.Row && pt.Column <= start.Column {
		return false
	}
	if pt.Row == end.Row && pt.Column >= end.Column {
		return false
	}
	return true
}

// ShouldExpandBraceBlock reports whether typing '{' at (line, col) should
// expand into an indented block ("{\n\t\n}") rather than a plain "{}" pair.
// It reparses content with "{}" inserted at the cursor and inspects the
// resulting node: constructs conventionally written inline (import/export
// lists, destructuring patterns, object literals, JSX expression
// containers) don't expand; everything else (function bodies, control-flow
// blocks, struct/class bodies, ...) does. It also never expands inside a
// string/template literal — a '{' typed there is string content, not the
// start of a block, so it shouldn't split the line.
func (h *Highlighter) ShouldExpandBraceBlock(content []byte, line, col int) bool {
	if h == nil {
		return true
	}
	if h.IsInString(content, line, col) {
		return false
	}

	lines := strings.Split(string(content), "\n")
	if line < 0 || line >= len(lines) {
		return true
	}
	// col is rune-based (matching the client's cursor column); tree-sitter
	// Points use byte columns, so convert against the pre-insertion line —
	// the inserted "{}" starts exactly at this boundary either way.
	byteCol := runeColToByteCol(lines[line], col)

	p := sitter.NewParser()
	p.SetLanguage(h.lang)
	tree, err := p.ParseString(context.Background(), nil, insertRunesAt(content, line, col, "{}"))
	if err != nil || tree == nil {
		return true
	}
	defer tree.Close()

	pt := sitter.Point{Row: uint(line), Column: uint(byteCol)}
	node := tree.RootNode().DescendantForPointRange(pt, pt)
	if node.IsNull() {
		return true
	}
	if tsInlineBraceTypes[node.Type()] {
		return false
	}
	if parent := node.Parent(); !parent.IsNull() && tsInlineBraceTypes[parent.Type()] {
		return false
	}
	return true
}

// insertRunesAt returns content with s inserted at the given rune-based
// (line, col) position, leaving content unchanged if the position is out of
// range.
func insertRunesAt(content []byte, line, col int, s string) []byte {
	lines := strings.Split(string(content), "\n")
	if line < 0 || line >= len(lines) {
		return content
	}
	runes := []rune(lines[line])
	if col < 0 || col > len(runes) {
		return content
	}
	lines[line] = string(runes[:col]) + s + string(runes[col:])
	return []byte(strings.Join(lines, "\n"))
}

// DedentTarget reports the indentation that a new line (created by pressing
// Enter at line, col) should use, for the specific case where the next
// token after the cursor — skipping same-line whitespace — closes the
// block/call/group enclosing the cursor (e.g. pressing Enter right before
// an already-typed "}" that has other content before it, so it isn't just
// an empty pair). When that's the case, the new line should align with the
// line the block opened on rather than inheriting the cursor line's
// (deeper) indentation. Returns ("", false) when there's no such token, or
// when this language has no indent query.
//
// This deliberately only reads the @indent.end / @indent.branch captures —
// the token-to-closer relationship these queries encode directly via tree
// structure (a captured closing token's parent is the node it closes) — and
// ignores the query language's predicates (#set!, #eq?, ...) and the
// @indent.begin/@indent.align/@indent.immediate machinery that give Helix's
// full algorithm its scope-opening and continuation-alignment behavior.
// That's out of scope here; see the indent design discussion for why.
func (h *Highlighter) DedentTarget(content []byte, line, col int) (string, bool) {
	if h == nil || h.indentQuery == nil {
		return "", false
	}

	lines := strings.Split(string(content), "\n")
	if line < 0 || line >= len(lines) {
		return "", false
	}
	runes := []rune(lines[line])
	if col < 0 || col > len(runes) {
		return "", false
	}
	c := col
	for c < len(runes) && (runes[c] == ' ' || runes[c] == '\t') {
		c++
	}
	if c >= len(runes) {
		return "", false // nothing but whitespace left on this line
	}
	// c is rune-based; tree-sitter Points use byte columns.
	targetRow, targetCol := uint(line), uint(runeColToByteCol(lines[line], c))

	p := sitter.NewParser()
	p.SetLanguage(h.lang)
	tree, err := p.ParseString(context.Background(), nil, content)
	if err != nil || tree == nil {
		return "", false
	}
	defer tree.Close()

	qc := sitter.NewQueryCursor()
	matches := qc.Matches(h.indentQuery, tree.RootNode(), content)
	for {
		m := matches.Next()
		if m == nil {
			break
		}
		for _, cap := range m.Captures {
			name := h.indentQuery.CaptureNameForID(cap.Index)
			if name != "indent.end" && name != "indent.branch" {
				continue
			}
			sp := cap.Node.StartPoint()
			if sp.Row != targetRow || sp.Column != targetCol {
				continue
			}
			target := cap.Node.Parent()
			if target.IsNull() {
				continue
			}
			targetLine := lineAt(lines, int(target.StartPoint().Row))
			return targetLine[:leadingWS(targetLine)], true
		}
	}
	return "", false
}

// leadingWS returns the number of leading space/tab bytes in s.
func leadingWS(s string) int {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func tsNodeSpan(n sitter.Node, lines []string) TextObject {
	sp, ep := n.StartPoint(), n.EndPoint()
	startCol := byteToRuneCol(lineAt(lines, int(sp.Row)), int(sp.Column))
	endCol := byteToRuneCol(lineAt(lines, int(ep.Row)), int(ep.Column))
	return TextObject{int(sp.Row), startCol, int(ep.Row), endCol}
}

func tsAncestor(node sitter.Node, types map[string]bool) (sitter.Node, bool) {
	for !node.IsNull() {
		if types[node.Type()] {
			return node, true
		}
		node = node.Parent()
	}
	return node, false
}

// blockInsideSpan returns the span of a block's content, excluding the brace lines.
// EndCol is set to math.MaxInt for multi-line blocks; callers should clamp to the
// actual line length before storing into a cursor or selection.
func blockInsideSpan(block sitter.Node, lines []string) TextObject {
	sp := block.StartPoint()
	ep := block.EndPoint()
	if ep.Row > sp.Row {
		return TextObject{int(sp.Row) + 1, 0, int(ep.Row) - 1, math.MaxInt}
	}
	// Single-line block: select between the braces. sp/ep are byte columns
	// on the same line; convert to rune columns before adjusting by ±1 so
	// the ±1 lands on a rune boundary rather than mid-character.
	line := lineAt(lines, int(sp.Row))
	sc, ec := byteToRuneCol(line, int(sp.Column))+1, byteToRuneCol(line, int(ep.Column))-1
	if ec < sc {
		ec = sc
	}
	return TextObject{int(sp.Row), sc, int(ep.Row), ec}
}

// findDescendantBlock does a depth-limited DFS for the first block-type child.
// A limit of 4 is enough for all real grammars (e.g. Go's type_declaration →
// type_spec → struct_type → field_declaration_list is 3 levels).
func findDescendantBlock(node sitter.Node, depth int) (sitter.Node, bool) {
	if depth == 0 {
		return sitter.Node{}, false
	}
	for i := range node.ChildCount() {
		child := node.Child(i)
		if tsBlockTypes[child.Type()] {
			return child, true
		}
		if result, found := findDescendantBlock(child, depth-1); found {
			return result, found
		}
	}
	return sitter.Node{}, false
}

func tsFunctionInside(node sitter.Node, lines []string) (TextObject, bool) {
	fn, ok := tsAncestor(node, tsFunctionTypes)
	if !ok {
		return TextObject{}, false
	}
	if block, found := findDescendantBlock(fn, 4); found {
		return blockInsideSpan(block, lines), true
	}
	return tsNodeSpan(fn, lines), true
}

func tsTypeInside(node sitter.Node, lines []string) (TextObject, bool) {
	tn, ok := tsAncestor(node, tsTypeTypes)
	if !ok {
		return TextObject{}, false
	}
	if block, found := findDescendantBlock(tn, 4); found {
		return blockInsideSpan(block, lines), true
	}
	return tsNodeSpan(tn, lines), true
}

// tsArgumentAt finds the argument-list child containing pt. pt is a
// byte-column tree-sitter Point (already converted by the caller), matching
// the byte columns of child.StartPoint()/EndPoint() compared against it.
func tsArgumentAt(node sitter.Node, pt sitter.Point, lines []string) (TextObject, bool) {
	argList, ok := tsAncestor(node, tsArgListTypes)
	if !ok {
		return TextObject{}, false
	}
	for i := range argList.NamedChildCount() {
		child := argList.NamedChild(i)
		sp, ep := child.StartPoint(), child.EndPoint()
		containsRow := sp.Row <= pt.Row && pt.Row <= ep.Row
		if !containsRow {
			continue
		}
		colOK := (sp.Row < pt.Row || sp.Column <= pt.Column) &&
			(ep.Row > pt.Row || ep.Column >= pt.Column)
		if colOK {
			return tsNodeSpan(child, lines), true
		}
	}
	return TextObject{}, false
}

func tsCommentAt(node sitter.Node, lines []string) (TextObject, bool) {
	cn, ok := tsAncestor(node, tsCommentTypes)
	if !ok {
		return TextObject{}, false
	}
	return tsNodeSpan(cn, lines), true
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

// runeColToByteCol converts a rune-based column into a byte offset within
// line — the inverse of byteToRuneCol. Needed because tree-sitter Points use
// byte columns, while the client's cursor columns are rune-based.
func runeColToByteCol(line string, runeCol int) int {
	if runeCol <= 0 {
		return 0
	}
	runes := []rune(line)
	if runeCol >= len(runes) {
		return len(line)
	}
	return len(string(runes[:runeCol]))
}

// --- capture → ANSI mapping ---

// hexToANSI converts a "#RRGGBB" hex color to a true-color SGR open sequence.
// HexToANSI converts a "#RRGGBB" color to its SGR truecolor escape sequence.
// Exported so other packages (e.g. semantic-token coloring) can reuse
// indigo's existing hex palette instead of maintaining a second one.
func HexToANSI(hex string) string { return hexToANSI(hex) }

// defaultFgANSI resets to the terminal's default foreground color — the
// fallback used when a theme supplies a color string hexToANSI can't parse.
const defaultFgANSI = "\x1b[39m"

func hexToANSI(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return defaultFgANSI
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return defaultFgANSI
	}
	r := (v >> 16) & 0xFF
	g := (v >> 8) & 0xFF
	b := v & 0xFF
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
	"constructor":           {hexToANSI("#4EC9B0"), 50},
	"namespace":             {hexToANSI("#4EC9B0"), 48},
	"module":                {hexToANSI("#4EC9B0"), 48},
	"tag":                   {hexToANSI("#569CD6"), 46},
	"attribute":             {hexToANSI("#9CDCFE"), 44},
	"variable.builtin":      {hexToANSI("#569CD6"), 42},
	"variable":              {hexToANSI("#9CDCFE"), 40},
	"label":                 {hexToANSI("#9CDCFE"), 38},
	"punctuation":           {hexToANSI("#D4D4D4"), 20},
	"punctuation.bracket":   {hexToANSI("#D4D4D4"), 20},
	"punctuation.delimiter": {hexToANSI("#D4D4D4"), 20},
	"punctuation.special":   {hexToANSI("#C586C0"), 20},
	"error":                 {hexToANSI("#F44747"), 95},

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
	"text":           {hexToANSI("#D4D4D4"), 27},
	"text.title":     {hexToANSI("#569CD6"), 87},
	"text.strong":    {hexToANSIBold("#E5C07B"), 83},
	"text.emphasis":  {hexToANSIItalic("#D7BA7D"), 83},
	"text.literal":   {hexToANSI("#d5890e"), 82},
	"text.uri":       {hexToANSI("#615bd3"), 78},
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
	// Progressively trim the last dot-separated segment, keeping the most
	// specific match still present in the table, one priority step down per
	// level trimmed — e.g. "function.method.private" falls back to
	// "function.method" (not straight to "function") if the former exists.
	prefix := name
	for level := 1; ; level++ {
		idx := strings.LastIndex(prefix, ".")
		if idx < 0 {
			return "", 0, false
		}
		prefix = prefix[:idx]
		if e, ok := captureTable[prefix]; ok {
			return e.ansi, e.priority - level, true
		}
	}
}
