//go:build (lang_all && !lang_not_gleam) || lang_gleam

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/gleam"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(gleam.GetLanguage()), gleam.GetQuery("highlights")
	}
	registerLang(fn, ".gleam")
}
