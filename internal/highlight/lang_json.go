//go:build (lang_all && !lang_not_json) || lang_json

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	json_lang "github.com/alexaandru/go-sitter-forest/json"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(json_lang.GetLanguage()), json_lang.GetQuery("highlights")
	}
	registerLang(fn, ".json")
}
