//go:build (lang_all && !lang_not_gleam) || lang_gleam

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/gleam"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(gleam.GetLanguage()), gleam.GetQuery("highlights")
	}
	registerLang(fn, ".gleam")
}
