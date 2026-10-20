//go:build (lang_all && !lang_not_scala) || lang_scala

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/scala"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(scala.GetLanguage()), scala.GetQuery("highlights")
	}
	registerLang(fn, ".scala", ".sc")
}
