//go:build (lang_all && !lang_not_lua) || lang_lua

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/lua"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(lua.GetLanguage()), lua.GetQuery("highlights")
	}
	registerLang(fn, ".lua")
}
