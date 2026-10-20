//go:build (lang_all && !lang_not_yaml) || lang_yaml

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/yaml"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(yaml.GetLanguage()), yaml.GetQuery("highlights")
	}
	registerLang(fn, ".yaml", ".yml")
}
