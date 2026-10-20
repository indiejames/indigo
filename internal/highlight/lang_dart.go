//go:build (lang_all && !lang_not_dart) || lang_dart

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/dart"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(dart.GetLanguage()), dart.GetQuery("highlights")
	}
	registerLang(fn, ".dart")
}
