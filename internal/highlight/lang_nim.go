//go:build (lang_all && !lang_not_nim) || lang_nim

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/nim"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(nim.GetLanguage()), nim.GetQuery("highlights")
	}
	registerLang(fn, ".nim")
}
