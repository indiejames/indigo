//go:build (lang_all && !lang_not_c) || lang_c

package highlight

import (
	c_lang "github.com/alexaandru/go-sitter-forest/c"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(c_lang.GetLanguage()), c_lang.GetQuery("highlights")
	}
	registerLang(fn, ".c", ".h")
	registerIndentQuery(func() []byte { return c_lang.GetQuery("indents") }, ".c", ".h")
}
