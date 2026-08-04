package highlight

// javascriptHighlightQuery is ported from Helix's
// runtime/queries/{ecma,_javascript}/highlights.scm (see ecmaHighlightQuery
// in query_ecma.go for the shared base), concatenated here because
// go-sitter-forest returns the raw upstream query text and this project's
// query engine doesn't resolve Helix's "; inherits:" directives. See
// THIRD_PARTY_NOTICES.md for source commit and license (MPL-2.0).
//
// Changes from upstream: backticks removed from comments (illegal inside a
// Go raw string literal); otherwise verbatim. "(#is-not? local)" predicates
// compile but are inert no-ops here — indigo has no locals-query pass.
const javascriptHighlightQuery = ecmaHighlightQuery + `
; Function and method parameters
;-------------------------------

; Javascript and Typescript Treesitter grammars deviate when defining the
; tree structure for parameters, so we need to address them in each specific
; language instead of ecma.

; (p)
(formal_parameters 
  (identifier) @variable.parameter)

; (...p)
(formal_parameters
  (rest_pattern
    (identifier) @variable.parameter))

; ({ p })
(formal_parameters
  (object_pattern
    (shorthand_property_identifier_pattern) @variable.parameter))

; ({ a: p })
(formal_parameters
  (object_pattern
    (pair_pattern
      value: (identifier) @variable.parameter)))

; ([ p ])
(formal_parameters
  (array_pattern
    (identifier) @variable.parameter))

; (p = 1)
(formal_parameters
  (assignment_pattern
    left: (identifier) @variable.parameter))
`
