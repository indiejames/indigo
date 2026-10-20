//go:build (lang_all && !lang_not_kotlin) || lang_kotlin

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/kotlin"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(kotlin.GetLanguage()), kotlin.GetQuery("highlights")
	}
	registerLang(fn, ".kt", ".kts")
}
