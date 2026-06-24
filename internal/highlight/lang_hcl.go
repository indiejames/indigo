//go:build (lang_all && !lang_not_hcl) || lang_hcl

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/hcl"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(hcl.GetLanguage()), hcl.GetQuery("highlights")
	}
	registerLang(fn, ".tf", ".hcl")
}
