//go:build (lang_all && !lang_not_elixir) || lang_elixir

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/elixir"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(elixir.GetLanguage()), elixir.GetQuery("highlights")
	}
	registerLang(fn, ".ex", ".exs")
}
