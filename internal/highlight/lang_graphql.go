//go:build (lang_all && !lang_not_graphql) || lang_graphql

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/graphql"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(graphql.GetLanguage()), graphql.GetQuery("highlights")
	}
	registerLang(fn, ".graphql", ".gql")
}
