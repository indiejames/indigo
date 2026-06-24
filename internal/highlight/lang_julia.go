//go:build (lang_all && !lang_not_julia) || lang_julia

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/julia"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(julia.GetLanguage()), julia.GetQuery("highlights")
	}
	registerLang(fn, ".jl")
}
