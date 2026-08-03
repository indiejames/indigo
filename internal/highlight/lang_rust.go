//go:build (lang_all && !lang_not_rust) || lang_rust

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/rust"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(rust.GetLanguage()), rust.GetQuery("highlights")
	}
	registerLang(fn, ".rs")
	registerIndentQuery(func() []byte { return rust.GetQuery("indents") }, ".rs")
}
