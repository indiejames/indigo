package highlight

// typescriptHighlightQuery is ported from Helix's
// runtime/queries/{ecma,_typescript}/highlights.scm (see ecmaHighlightQuery
// in query_ecma.go for the shared base), concatenated here because
// go-sitter-forest returns the raw upstream query text and this project's
// query engine doesn't resolve Helix's "; inherits:" directives. See
// THIRD_PARTY_NOTICES.md for source commit and license (MPL-2.0).
//
// Changes from upstream: backticks removed from comments (illegal inside a
// Go raw string literal); otherwise verbatim. "(#is-not? local)" predicates
// compile but are inert no-ops here — indigo has no locals-query pass, so a
// few upstream refinements (e.g. not re-coloring a builtin name shadowed by
// a local variable) don't apply.
const typescriptHighlightQuery = ecmaHighlightQuery + `
; Namespaces
; ----------

(internal_module
  [((identifier) @namespace) ((nested_identifier (identifier) @namespace))])

(ambient_declaration "global" @namespace)

; Parameters
; ----------
; Javascript and Typescript Treesitter grammars deviate when defining the
; tree structure for parameters, so we need to address them in each specific
; language instead of ecma.

; (p: t)
; (p: t = 1)
(required_parameter 
  (identifier) @variable.parameter)

; (...p: t)
(required_parameter
  (rest_pattern
    (identifier) @variable.parameter))

; ({ p }: { p: t })
(required_parameter
  (object_pattern
    (shorthand_property_identifier_pattern) @variable.parameter))

; ({ a: p }: { a: t })
(required_parameter
  (object_pattern
    (pair_pattern
      value: (identifier) @variable.parameter)))

; ({ p = default }: { p: t = default })
(required_parameter
  (object_pattern
    (object_assignment_pattern
      left: (shorthand_property_identifier_pattern) @variable.parameter)))

; ({ a: p = default }: { a: t = default })
(required_parameter
  (object_pattern
    (pair_pattern
      value: (assignment_pattern
        left: (identifier) @variable.parameter))))

; ([ p ]: t[])
(required_parameter
  (array_pattern
    (identifier) @variable.parameter))

; (p?: t)
; (p?: t = 1) // Invalid but still possible to highlight.
(optional_parameter 
  (identifier) @variable.parameter)

; (...p?: t) // Invalid but still possible to highlight.
(optional_parameter
  (rest_pattern
    (identifier) @variable.parameter))

; ({ p }: { p?: t})
(optional_parameter
  (object_pattern
    (shorthand_property_identifier_pattern) @variable.parameter))

; ({ a: p }: { a?: t })
(optional_parameter
  (object_pattern
    (pair_pattern
      value: (identifier) @variable.parameter)))

; ({ p = default }: { p?: t = default })
(optional_parameter
  (object_pattern
    (object_assignment_pattern
      left: (shorthand_property_identifier_pattern) @variable.parameter)))

; ({ a: p = default }: { a?: t = default })
(optional_parameter
  (object_pattern
    (pair_pattern
      value: (assignment_pattern
        left: (identifier) @variable.parameter))))

; ([ p ]?: t[]) // Invalid but still possible to highlight.
(optional_parameter
  (array_pattern
    (identifier) @variable.parameter))

(public_field_definition) @punctuation.special
(this_type) @variable.builtin
(type_predicate) @keyword.operator

; Punctuation
; -----------

[
  ":"
] @punctuation.delimiter

(optional_parameter "?" @punctuation.special)
(property_signature "?" @punctuation.special)

(conditional_type ["?" ":"] @operator)
(ternary_expression ["?" ":"] @operator)

; Keywords
; --------

[
  "abstract"
  "accessor"
  "declare"
  "module"
  "infer"
  "implements"
  "keyof"
  "namespace"
  "override"
  "satisfies"
  "using"
] @keyword

; asserts in a return-type type predicate, e.g. function f(x): asserts x is T
"asserts" @keyword.operator

[
  "export"
  "from"
] @keyword.control.import

[
  "type"
  "interface"
  "enum"
] @keyword.storage.type

[
  "public"
  "private"
  "protected"
  "readonly"
] @keyword.storage.modifier

; Types
; -----

(type_identifier) @type
(type_parameter
  name: (type_identifier) @type.parameter)
(predefined_type) @type.builtin

; Type arguments and parameters
; -----------------------------

(type_arguments
  [
    "<"
    ">"
  ] @punctuation.bracket)

(type_parameters
  [
    "<"
    ">"
  ] @punctuation.bracket)

(omitting_type_annotation) @punctuation.special
(opting_type_annotation) @punctuation.special

; Literals
; --------

[
  (template_literal_type)
] @string

(import_require_clause
  (identifier) "="
  ("require") @keyword)

; Method signatures in interfaces / type literals, and function-typed
; property signatures (foo(): void, bar: () => void).
(method_signature
  name: (property_identifier) @function.method)
(abstract_method_signature
  name: (property_identifier) @function.method)
(property_signature
  name: (property_identifier) @function.method
  type: (type_annotation
    [
      (function_type)
      (union_type (parenthesized_type (function_type)))
    ]))
`

// tsxHighlightQuery extends typescriptHighlightQuery with JSX element and
// attribute captures, ported from Helix's runtime/queries/_jsx/highlights.scm,
// for the .tsx grammar. See THIRD_PARTY_NOTICES.md.
const tsxHighlightQuery = typescriptHighlightQuery + `
; Punctuation
; -----------

; Handle attribute delimiter (<Component color="red"/>)
(jsx_attribute "=" @punctuation.delimiter)

; <Component>
(jsx_opening_element ["<" ">"] @punctuation.bracket)

; </Component>
(jsx_closing_element ["</" ">"] @punctuation.bracket)

; <Component />
(jsx_self_closing_element ["<" "/>"] @punctuation.bracket)

; Attributes
; ----------

(jsx_attribute (property_identifier) @attribute)

; Opening elements
; ----------------

(jsx_opening_element (identifier) @tag)

(jsx_opening_element ((identifier) @constructor
 (#match? @constructor "^[A-Z]")))

; Closing elements
; ----------------

(jsx_closing_element (identifier) @tag)

(jsx_closing_element ((identifier) @constructor
 (#match? @constructor "^[A-Z]")))

; Self-closing elements
; ---------------------

(jsx_self_closing_element (identifier) @tag)

(jsx_self_closing_element ((identifier) @constructor
 (#match? @constructor "^[A-Z]")))
`
