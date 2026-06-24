//go:build (lang_all && !lang_not_cpp) || lang_cpp

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/cpp"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(cpp.GetLanguage()), []byte(cppHighlightQuery)
	}
	registerLang(fn, ".cc", ".cpp", ".cxx", ".c++", ".hh", ".hpp", ".hxx")
}
