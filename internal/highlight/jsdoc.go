package highlight

import (
	"context"
	"strings"

	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

// JSDocParam describes one parameter of a JS/TS function for JSDoc template
// generation. Type is empty when the parameter has no TS type annotation.
type JSDocParam struct {
	Name string
	Type string
}

// JSDocSignature describes a JS/TS function signature for JSDoc template
// generation. ReturnType is empty when the function has no TS return-type
// annotation, or when it's declared/inferred void.
type JSDocSignature struct {
	Params     []JSDocParam
	ReturnType string
}

// FunctionSignatureAt finds the function-like node whose declaration begins
// on line (0-based) — a function_declaration, method_definition, or
// arrow_function, covering both `function foo(...)` / class-method form and
// `const foo = (...) => ...` form — and returns its parameters and return
// type for JSDoc template generation. ok is false if no such node starts
// exactly on that line.
func (h *Highlighter) FunctionSignatureAt(content []byte, line int) (JSDocSignature, bool) {
	if h == nil || line < 0 {
		return JSDocSignature{}, false
	}
	p := sitter.NewParser()
	p.SetLanguage(h.lang)
	tree, err := p.ParseString(context.Background(), nil, content)
	if err != nil || tree == nil {
		return JSDocSignature{}, false
	}
	defer tree.Close()

	fn, ok := findFunctionOnRow(tree.RootNode(), uint(line))
	if !ok {
		return JSDocSignature{}, false
	}
	return jsdocSignatureFor(fn, content), true
}

// findFunctionOnRow searches node's subtree for the outermost tsFunctionTypes
// node whose declaration starts exactly on row, pruning any subtree whose
// span doesn't cover row.
func findFunctionOnRow(node sitter.Node, row uint) (sitter.Node, bool) {
	sp, ep := node.StartPoint(), node.EndPoint()
	if row < sp.Row || row > ep.Row {
		return sitter.Node{}, false
	}
	if tsFunctionTypes[node.Type()] && sp.Row == row {
		return node, true
	}
	for i := range node.ChildCount() {
		if found, ok := findFunctionOnRow(node.Child(i), row); ok {
			return found, true
		}
	}
	return sitter.Node{}, false
}

// jsdocSignatureFor extracts params/return type from a tsFunctionTypes node
// (function_declaration, method_definition, or arrow_function — all expose
// "parameters" and "return_type" fields in the JS/TS grammars).
func jsdocSignatureFor(fn sitter.Node, content []byte) JSDocSignature {
	var sig JSDocSignature
	if params := fn.ChildByFieldName("parameters"); !params.IsNull() {
		for i := range params.NamedChildCount() {
			if p, ok := jsdocParam(params.NamedChild(i), content); ok {
				sig.Params = append(sig.Params, p)
			}
		}
	}
	if rt := fn.ChildByFieldName("return_type"); !rt.IsNull() {
		txt := strings.TrimSpace(strings.TrimPrefix(collapseWS(rt.Content(content)), ":"))
		if txt != "" && txt != "void" {
			sig.ReturnType = txt
		}
	}
	return sig
}

// jsdocParam extracts one JSDoc param from a formal_parameters named child.
// A defaulted or TS-optional (`?`) parameter is bracketed per JSDoc
// convention: "[name]" or "[name=default]".
func jsdocParam(n sitter.Node, content []byte) (JSDocParam, bool) {
	switch n.Type() {
	case "identifier":
		return JSDocParam{Name: collapseWS(n.Content(content))}, true

	case "required_parameter", "optional_parameter":
		pattern := n.ChildByFieldName("pattern")
		if pattern.IsNull() {
			return JSDocParam{}, false
		}
		name := collapseWS(pattern.Content(content))
		typ := ""
		if tn := n.ChildByFieldName("type"); !tn.IsNull() {
			typ = strings.TrimSpace(strings.TrimPrefix(collapseWS(tn.Content(content)), ":"))
		}
		optional := n.Type() == "optional_parameter"
		if val := n.ChildByFieldName("value"); !val.IsNull() {
			optional = true
			name += "=" + collapseWS(val.Content(content))
		}
		if optional {
			name = "[" + name + "]"
		}
		return JSDocParam{Name: name, Type: typ}, true

	case "assignment_pattern":
		left := n.ChildByFieldName("left")
		right := n.ChildByFieldName("right")
		if left.IsNull() {
			return JSDocParam{}, false
		}
		name := collapseWS(left.Content(content))
		if !right.IsNull() {
			name += "=" + collapseWS(right.Content(content))
		}
		return JSDocParam{Name: "[" + name + "]"}, true

	default:
		// rest_pattern ("...rest"), object_pattern/array_pattern
		// (destructuring with no wrapping required_parameter, plain JS), or
		// anything else: fall back to the node's own collapsed text.
		txt := collapseWS(n.Content(content))
		if txt == "" {
			return JSDocParam{}, false
		}
		return JSDocParam{Name: txt}, true
	}
}

// collapseWS replaces any run of whitespace (including newlines, for a
// type or default spanning multiple source lines) with a single space.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
