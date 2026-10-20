//go:build (lang_all && !lang_not_toml) || lang_toml

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/toml"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(toml.GetLanguage()), toml.GetQuery("highlights")
	}
	registerLang(fn, ".toml")
}
