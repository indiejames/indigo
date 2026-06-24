//go:build (lang_all && !lang_not_scala) || lang_scala

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/scala"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(scala.GetLanguage()), scala.GetQuery("highlights")
	}
	registerLang(fn, ".scala", ".sc")
}
