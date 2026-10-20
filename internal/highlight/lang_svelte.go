//go:build (lang_all && !lang_not_svelte) || lang_svelte

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/svelte"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(svelte.GetLanguage()), []byte(svelteHighlightQuery)
	}
	registerLang(fn, ".svelte")
}
