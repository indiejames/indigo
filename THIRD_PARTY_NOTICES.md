# Third-party notices

indigo is MIT licensed (see `LICENSE`). Some tree-sitter highlight queries in
`internal/highlight/` are ported from other projects under different
licenses. This file lists those ports. Per-file attribution comments in the
source point back here.

## Helix editor query ports (MPL-2.0)

Source: https://github.com/helix-editor/helix, commit
`079a789e8cb08ead67f19e1971a1b7438b37354b`, licensed under the Mozilla Public
License 2.0 (https://www.mozilla.org/en-US/MPL/2.0/). A copy of the license
is available at https://github.com/helix-editor/helix/blob/master/LICENSE.

indigo's tree-sitter grammars come from `go-sitter-forest`, which vendors
each grammar's own bundled `highlights.scm`. Those are often minimal or use
nvim-treesitter's `; inherits: <base>` directive to pull in a shared base
query — a mechanism indigo's query engine (`go-tree-sitter-bare`) does not
resolve. Where that left a real gap in highlighting quality, the file below
ports Helix's own curated queries instead, flattening any `inherits` chain
by hand and adjusting for node-type differences in the specific grammar
version indigo vendors.

Per the MPL's file-level copyleft (section 3.1), the query text embedded in
each file below — not indigo as a whole — is Covered Software under the
MPL-2.0. If you modify the query text in these files, MPL 3.1 applies to
your modifications; unrelated files in this repository are unaffected.

| indigo file | Upstream source(s) | Notes |
|---|---|---|
| `internal/highlight/query_typescript.go` | `runtime/queries/{ecma,_typescript}/highlights.scm` | `.ts` |
| `internal/highlight/query_typescript.go` (`tsxHighlightQuery`) | `runtime/queries/{ecma,_typescript,_jsx}/highlights.scm` | `.tsx` |
| `internal/highlight/query_javascript.go` | `runtime/queries/{ecma,_javascript}/highlights.scm` | `.js`, `.mjs`, `.cjs` |
| `internal/highlight/query_cpp.go` | `runtime/queries/{c,cpp}/highlights.scm` | `.cc`, `.cpp`, `.cxx`, `.c++`, `.hh`, `.hpp`, `.hxx`; also covers plain C via the shared base |
| `internal/highlight/query_html.go` | `runtime/queries/html/highlights.scm` | `.html`, `.htm`; also the base for the Svelte port below |
| `internal/highlight/query_php.go` | `runtime/queries/php/highlights.scm` | `.php` |
| `internal/highlight/query_gdscript.go` | `runtime/queries/gdscript/highlights.scm` | `.gd` |
| `internal/highlight/query_svelte.go` | `runtime/queries/html/highlights.scm` (partial — see below) | `.svelte` |

All ports are verbatim except for mechanical fixes needed to compile against
the specific grammar version `go-sitter-forest` vendors (backticks stripped
from comments, a handful of node types/tokens not present in that grammar
version removed). Each file's header comment documents its specific
deviations from upstream.

`(#is-not? local)` predicates, used by several of these queries to avoid
re-coloring a builtin/global name that's been shadowed by a local variable,
compile successfully but are inert no-ops under indigo's query engine —
indigo has no locals-query resolution pass (no `locals.scm` support), unlike
Helix. This is a known, accepted gap: it only affects the rare case of a
local variable shadowing a builtin name.

### Svelte is a hybrid, not a full port

Helix's actual `runtime/queries/svelte/highlights.scm` targets a newer
tree-sitter-svelte grammar (nodes for `tag_namespace`, `attribute_directive`,
`snippet_block`, and other Svelte 5 syntax) than the version vendored by
`go-sitter-forest` (`svelte@v1.9.2`) has — that query fails to compile
against indigo's grammar. `query_svelte.go` instead combines:

1. The Helix HTML port above (`query_html.go`) — the vendored
   tree-sitter-svelte grammar inherits HTML's node shapes for tags,
   attributes, and text, and upstream Svelte's own query is written to layer
   on top of an HTML query for exactly that reason.
2. The Svelte-specific fragment (control-flow keywords, block punctuation)
   bundled with the vendored grammar itself, at
   `github.com/alexaandru/go-sitter-forest/svelte@v1.9.2/highlights.scm`,
   MIT licensed — see that module's own `LICENSE` file (Copyright (c) 2019
   Maxim Sukharev, 2024 Alex Ungur).

## Updating these ports

If `go-sitter-forest` upgrades a grammar (e.g. to a newer tree-sitter-svelte
that supports Helix's real Svelte query), re-check whether the corresponding
Helix query now compiles cleanly and replace the hand-adapted version above.
