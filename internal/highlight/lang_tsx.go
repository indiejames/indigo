//go:build (lang_all && !lang_not_tsx) || lang_tsx

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/tsx"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(tsx.GetLanguage()), []byte(tsxHighlightQuery)
	}
	registerLang(fn, ".tsx")
}
