//go:build (lang_all && !lang_not_python) || lang_python

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/python"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(python.GetLanguage()), python.GetQuery("highlights")
	}
	registerLang(fn, ".py")
}
