//go:build (lang_all && !lang_not_bash) || lang_bash

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/bash"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(bash.GetLanguage()), bash.GetQuery("highlights")
	}
	registerLang(fn, ".sh", ".bash")
}
