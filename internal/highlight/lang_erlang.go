//go:build (lang_all && !lang_not_erlang) || lang_erlang

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/erlang"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(erlang.GetLanguage()), erlang.GetQuery("highlights")
	}
	registerLang(fn, ".erl", ".hrl")
}
