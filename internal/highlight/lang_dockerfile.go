//go:build (lang_all && !lang_not_dockerfile) || lang_dockerfile

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/dockerfile"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(dockerfile.GetLanguage()), dockerfile.GetQuery("highlights")
	}
	registerLang(fn, "dockerfile")
}
