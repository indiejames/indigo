//go:build (lang_all && !lang_not_dart) || lang_dart

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/dart"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(dart.GetLanguage()), dart.GetQuery("highlights")
	}
	registerLang(fn, ".dart")
}
