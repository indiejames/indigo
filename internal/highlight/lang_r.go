//go:build (lang_all && !lang_not_r) || lang_r

package highlight

import (
	r_lang "github.com/alexaandru/go-sitter-forest/r"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(r_lang.GetLanguage()), r_lang.GetQuery("highlights")
	}
	registerLang(fn, ".r")
}
