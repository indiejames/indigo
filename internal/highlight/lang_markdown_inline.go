//go:build (lang_all && !lang_not_markdown) || lang_markdown

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/markdown_inline"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		// Use NativeFirst: the nvimts query contains Neovim-specific #set! predicates
		// that go-tree-sitter-bare rejects at query-compile time.
		return sitter.NewLanguage(markdown_inline.GetLanguage()), markdown_inline.GetQuery("highlights", markdown_inline.NativeFirst)
	}
	registerExtraLang(fn, ".md", ".markdown")
}
