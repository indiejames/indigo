//go:build (lang_all && !lang_not_graphql) || lang_graphql

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/graphql"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(graphql.GetLanguage()), graphql.GetQuery("highlights")
	}
	registerLang(fn, ".graphql", ".gql")
}
