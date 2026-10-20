//go:build (lang_all && !lang_not_javascript) || lang_javascript

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/javascript"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(javascript.GetLanguage()), []byte(javascriptHighlightQuery)
	}
	registerLang(fn, ".js", ".mjs", ".cjs", ".jsx")
}
