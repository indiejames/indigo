//go:build (lang_all && !lang_not_html) || lang_html

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/html"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(html.GetLanguage()), []byte(htmlHighlightQuery)
	}
	registerLang(fn, ".html", ".htm")
}
