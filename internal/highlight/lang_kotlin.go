//go:build (lang_all && !lang_not_kotlin) || lang_kotlin

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/kotlin"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(kotlin.GetLanguage()), kotlin.GetQuery("highlights")
	}
	registerLang(fn, ".kt", ".kts")
}
