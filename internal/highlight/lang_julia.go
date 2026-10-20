//go:build (lang_all && !lang_not_julia) || lang_julia

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/julia"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(julia.GetLanguage()), julia.GetQuery("highlights")
	}
	registerLang(fn, ".jl")
}
