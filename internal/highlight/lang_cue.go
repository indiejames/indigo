//go:build (lang_all && !lang_not_cue) || lang_cue

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/cue"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(cue.GetLanguage()), cue.GetQuery("highlights")
	}
	registerLang(fn, ".cue")
}
