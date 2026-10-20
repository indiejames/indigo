//go:build (lang_all && !lang_not_elm) || lang_elm

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/elm"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(elm.GetLanguage()), elm.GetQuery("highlights")
	}
	registerLang(fn, ".elm")
}
