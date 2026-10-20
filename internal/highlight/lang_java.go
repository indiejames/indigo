//go:build (lang_all && !lang_not_java) || lang_java

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/java"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(java.GetLanguage()), java.GetQuery("highlights")
	}
	registerLang(fn, ".java")
}
