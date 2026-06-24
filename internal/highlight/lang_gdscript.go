//go:build (lang_all && !lang_not_gdscript) || lang_gdscript

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/gdscript"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(gdscript.GetLanguage()), []byte(gdscriptHighlightQuery)
	}
	registerLang(fn, ".gd")
}
