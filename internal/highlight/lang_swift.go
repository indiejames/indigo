//go:build (lang_all && !lang_not_swift) || lang_swift

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/swift"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(swift.GetLanguage()), swift.GetQuery("highlights")
	}
	registerLang(fn, ".swift")
}
