//go:build (lang_all && !lang_not_make) || lang_make

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/make"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(make.GetLanguage()), make.GetQuery("highlights")
	}
	registerLang(fn, "makefile", ".mk", ".mak", ".make")
}
