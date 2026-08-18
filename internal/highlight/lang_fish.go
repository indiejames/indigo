//go:build (lang_all && !lang_not_fish) || lang_fish

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/fish"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(fish.GetLanguage()), fish.GetQuery("highlights")
	}
	registerLang(fn, ".fish")
}
