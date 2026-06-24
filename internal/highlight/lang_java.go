//go:build (lang_all && !lang_not_java) || lang_java

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/java"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(java.GetLanguage()), java.GetQuery("highlights")
	}
	registerLang(fn, ".java")
}
