//go:build (lang_all && !lang_not_r) || lang_r

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	r_lang "github.com/alexaandru/go-sitter-forest/r"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(r_lang.GetLanguage()), r_lang.GetQuery("highlights")
	}
	registerLang(fn, ".r")
}
