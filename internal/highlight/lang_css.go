//go:build (lang_all && !lang_not_css) || lang_css

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/css"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(css.GetLanguage()), css.GetQuery("highlights")
	}
	registerLang(fn, ".css")
}
