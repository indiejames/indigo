//go:build (lang_all && !lang_not_dockerfile) || lang_dockerfile

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/dockerfile"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(dockerfile.GetLanguage()), dockerfile.GetQuery("highlights")
	}
	registerLang(fn, "dockerfile")
}
