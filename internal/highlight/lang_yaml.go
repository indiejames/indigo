//go:build (lang_all && !lang_not_yaml) || lang_yaml

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/yaml"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(yaml.GetLanguage()), yaml.GetQuery("highlights")
	}
	registerLang(fn, ".yaml", ".yml")
}
