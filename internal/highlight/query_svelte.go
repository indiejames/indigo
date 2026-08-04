package highlight

// svelteHighlightQuery is a hybrid, not a straight Helix port like the other
// files in this package. Helix's own runtime/queries/svelte/highlights.scm
// targets a newer tree-sitter-svelte grammar (tag_name, tag_namespace,
// attribute_directive, snippet_block, and other nodes for Svelte 5 syntax)
// that the version vendored via go-sitter-forest doesn't have — that query
// fails to compile here.
//
// Instead this combines:
//  1. htmlHighlightQuery (query_html.go) — the vendored tree-sitter-svelte
//     grammar inherits html's node shapes for tags/attributes/text, and
//     Svelte's own upstream query is written to layer on top of Helix's html
//     query for exactly that reason.
//  2. The svelte-specific fragment (control-flow keywords: if/each/await/
//     key/snippet, block punctuation) bundled with the go-sitter-forest
//     svelte grammar itself, at
//     github.com/alexaandru/go-sitter-forest/svelte@v1.9.2/highlights.scm
//     (MIT license — see that module's LICENSE file).
//
// See THIRD_PARTY_NOTICES.md for the html portion's source commit and
// license (MPL-2.0).
const svelteHighlightQuery = `
(tag_name) @tag
(erroneous_end_tag_name) @error
(doctype) @constant
(attribute_name) @attribute
(entity) @string.special.symbol
(comment) @comment

((attribute
  (attribute_name) @attribute
  (quoted_attribute_value (attribute_value) @markup.link.url))
 (#any-of? @attribute "href" "src"))

((element
  (start_tag
    (tag_name) @tag)
  (text) @markup.link.label)
  (#eq? @tag "a"))

(attribute [(attribute_value) (quoted_attribute_value)] @string)

((element
  (start_tag
    (tag_name) @tag)
  (text) @markup.bold)
  (#any-of? @tag "strong" "b"))

((element
  (start_tag
    (tag_name) @tag)
  (text) @markup.italic)
  (#any-of? @tag "em" "i"))

((element
  (start_tag
    (tag_name) @tag)
  (text) @markup.strikethrough)
  (#any-of? @tag "s" "del"))

[
  "<"
  ">"
  "</"
  "/>"
  "<!"
] @punctuation.bracket

"=" @punctuation.delimiter

(raw_text) @none

[
  "as"
  "key"
  "html"
  "snippet"
  "render"
] @keyword

"const" @type.qualifier

[
  "if"
  "else"
  "then"
] @keyword.conditional

"each" @keyword.repeat

[
  "await"
  "then"
] @keyword.coroutine

"catch" @keyword.exception

"debug" @keyword.debug

[
  "{"
  "}"
] @punctuation.bracket

[
  "#"
  ":"
  "/"
  "@"
] @tag.delimiter
`
