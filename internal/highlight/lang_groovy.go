//go:build (lang_all && !lang_not_groovy) || lang_groovy

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/groovy"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(groovy.GetLanguage()), groovy.GetQuery("highlights")
	}
	registerLang(fn, ".groovy", ".gvy", ".gy", ".gsh")
}
