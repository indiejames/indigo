//go:build (lang_all && !lang_not_lua) || lang_lua

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/lua"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(lua.GetLanguage()), lua.GetQuery("highlights")
	}
	registerLang(fn, ".lua")
}
