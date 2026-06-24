//go:build (lang_all && !lang_not_cue) || lang_cue

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/cue"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(cue.GetLanguage()), cue.GetQuery("highlights")
	}
	registerLang(fn, ".cue")
}
