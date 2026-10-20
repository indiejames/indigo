//go:build (lang_all && !lang_not_php) || lang_php

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/php"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(php.GetLanguage()), []byte(phpHighlightQuery)
	}
	registerLang(fn, ".php")
}
