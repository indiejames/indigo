//go:build (lang_all && !lang_not_svelte) || lang_svelte

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/svelte"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(svelte.GetLanguage()), []byte(svelteHighlightQuery)
	}
	registerLang(fn, ".svelte")
}
