//go:build (lang_all && !lang_not_ocaml) || lang_ocaml

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/ocaml"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(ocaml.GetLanguage()), ocaml.GetQuery("highlights")
	}
	registerLang(fn, ".ml", ".mli")
}
