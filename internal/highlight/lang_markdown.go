//go:build (lang_all && !lang_not_markdown) || lang_markdown

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/markdown"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(markdown.GetLanguage()), markdown.GetQuery("highlights")
	}
	registerLang(fn, ".md", ".markdown")
}
