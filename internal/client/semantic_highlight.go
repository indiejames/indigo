package client

import (
	"strings"

	"github.com/indiejames/indigo/internal/highlight"
)

// semanticTypeHex maps an LSP semantic-token type name to a color, reusing
// indigo's existing tree-sitter syntax palette where there's a clean
// correspondence — keeps semantic-token coloring visually consistent with the
// rest of the theme instead of introducing a second palette — and VS Code's
// own default semantic-highlighting convention where there isn't (parameter/
// property/label render identically to variable in VS Code's own built-in
// theme; enumMember like a constant).
//
// Token types outside this set (keyword, string, comment, number, operator,
// etc.) are deliberately NOT mapped: tree-sitter already colors those well,
// and a token can never simultaneously occupy the same position as one of
// them (a position can't be both a comment and a variable), so omitting them
// here just means they're silently left to tree-sitter rather than fought
// over.
var semanticTypeHex = map[string]string{
	"function": "#DCDCAA",
	"method":   "#DCDCAA",
	"macro":    "#DCDCAA",

	"variable":   "#9CDCFE",
	"parameter":  "#9CDCFE",
	"property":   "#9CDCFE",
	"label":      "#9CDCFE",
	"enumMember": "#9CDCFE",

	"class":         "#4EC9B0",
	"interface":     "#4EC9B0",
	"struct":        "#4EC9B0",
	"enum":          "#4EC9B0",
	"type":          "#4EC9B0",
	"typeParameter": "#4EC9B0",
	"namespace":     "#4EC9B0",
}

// semanticModifierPrefix returns SGR codes for the subset of LSP semantic
// modifiers indigo renders specially — tree-sitter has no equivalent concept
// (it parses syntax, not semantics), so this is the actual payoff of semantic
// tokens beyond just recoloring identifiers.
func semanticModifierPrefix(mods []string) string {
	var sb strings.Builder
	for _, m := range mods {
		switch m {
		case "readonly":
			sb.WriteString("\x1b[3m") // italic
		case "static":
			sb.WriteString("\x1b[1m") // bold
		case "defaultLibrary":
			sb.WriteString("\x1b[2m") // dim
		}
	}
	return sb.String()
}

// buildSemanticSpans converts semantic tokens into a highlight.LineSpans-
// shaped map. Only identifier-ish token types present in semanticTypeHex are
// converted; the rest are dropped (see semanticTypeHex's doc comment). The
// result is meant to be prepended ahead of tree-sitter's own spans for the
// same line at render time, so it wins for the positions it covers.
func buildSemanticSpans(tokens []ClientSemanticToken) highlight.LineSpans {
	if len(tokens) == 0 {
		return nil
	}
	out := make(highlight.LineSpans)
	for _, tok := range tokens {
		hex, ok := semanticTypeHex[tok.TokenType]
		if !ok || tok.Length <= 0 {
			continue
		}
		ansi := semanticModifierPrefix(tok.Modifiers) + highlight.HexToANSI(hex)
		out[tok.Line] = append(out[tok.Line], highlight.Span{
			StartCol: tok.Col,
			EndCol:   tok.Col + tok.Length,
			ANSI:     ansi,
		})
	}
	return out
}
