//go:build (lang_all && !lang_not_asm) || lang_asm

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/asm"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(asm.GetLanguage()), asm.GetQuery("highlights")
	}
	registerLang(fn, ".s", ".asm")
}
