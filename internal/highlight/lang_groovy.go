//go:build (lang_all && !lang_not_groovy) || lang_groovy

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/groovy"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(groovy.GetLanguage()), groovy.GetQuery("highlights")
	}
	registerLang(fn, ".groovy", ".gvy", ".gy", ".gsh")
}
