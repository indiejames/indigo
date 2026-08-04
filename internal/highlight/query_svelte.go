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
const svelteHighlightQuery = htmlHighlightQuery + `
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
