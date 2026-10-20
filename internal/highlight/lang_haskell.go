//go:build (lang_all && !lang_not_haskell) || lang_haskell

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/haskell"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(haskell.GetLanguage()), haskell.GetQuery("highlights")
	}
	registerLang(fn, ".hs", ".lhs")
}
