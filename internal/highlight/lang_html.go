//go:build (lang_all && !lang_not_html) || lang_html

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/html"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(html.GetLanguage()), []byte(htmlHighlightQuery)
	}
	registerLang(fn, ".html", ".htm")
}
