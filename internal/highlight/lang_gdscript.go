//go:build (lang_all && !lang_not_gdscript) || lang_gdscript

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/gdscript"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(gdscript.GetLanguage()), []byte(gdscriptHighlightQuery)
	}
	registerLang(fn, ".gd")
}
