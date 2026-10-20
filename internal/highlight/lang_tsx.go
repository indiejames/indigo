//go:build (lang_all && !lang_not_tsx) || lang_tsx

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/tsx"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(tsx.GetLanguage()), []byte(tsxHighlightQuery)
	}
	registerLang(fn, ".tsx")
}
