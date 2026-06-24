//go:build (lang_all && !lang_not_nix) || lang_nix

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/nix"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(nix.GetLanguage()), nix.GetQuery("highlights")
	}
	registerLang(fn, ".nix")
}
