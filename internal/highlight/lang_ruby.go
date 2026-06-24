//go:build (lang_all && !lang_not_ruby) || lang_ruby

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/ruby"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(ruby.GetLanguage()), ruby.GetQuery("highlights")
	}
	registerLang(fn, ".rb")
}
