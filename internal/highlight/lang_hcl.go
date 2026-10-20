//go:build (lang_all && !lang_not_hcl) || lang_hcl

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/hcl"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(hcl.GetLanguage()), hcl.GetQuery("highlights")
	}
	registerLang(fn, ".tf", ".hcl")
}
