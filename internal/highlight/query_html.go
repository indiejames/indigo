package highlight

// htmlHighlightQuery is ported verbatim from Helix's
// runtime/queries/html/highlights.scm. See THIRD_PARTY_NOTICES.md for source
// commit and license (MPL-2.0). No node-type incompatibilities found against
// the vendored tree-sitter-html grammar.
const htmlHighlightQuery = `
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
`
