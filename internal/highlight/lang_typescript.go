//go:build (lang_all && !lang_not_typescript) || lang_typescript

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/typescript"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(typescript.GetLanguage()), []byte(typescriptHighlightQuery)
	}
	registerLang(fn, ".ts")
}
