//go:build (lang_all && !lang_not_go) || lang_go

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	golang_lang "github.com/alexaandru/go-sitter-forest/go"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(golang_lang.GetLanguage()), golang_lang.GetQuery("highlights")
	}
	registerLang(fn, ".go")
	registerIndentQuery(func() []byte { return golang_lang.GetQuery("indents") }, ".go")
}
