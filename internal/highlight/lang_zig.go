//go:build (lang_all && !lang_not_zig) || lang_zig

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/zig"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(zig.GetLanguage()), zig.GetQuery("highlights")
	}
	registerLang(fn, ".zig")
}
