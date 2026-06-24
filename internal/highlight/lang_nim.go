//go:build (lang_all && !lang_not_nim) || lang_nim

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/nim"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(nim.GetLanguage()), nim.GetQuery("highlights")
	}
	registerLang(fn, ".nim")
}
