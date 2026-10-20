//go:build (lang_all && !lang_not_csharp) || lang_csharp

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/c_sharp"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(c_sharp.GetLanguage()), c_sharp.GetQuery("highlights")
	}
	registerLang(fn, ".cs")
}
