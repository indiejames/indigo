//go:build (lang_all && !lang_not_erlang) || lang_erlang

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/erlang"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(erlang.GetLanguage()), erlang.GetQuery("highlights")
	}
	registerLang(fn, ".erl", ".hrl")
}
