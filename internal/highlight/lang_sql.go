//go:build (lang_all && !lang_not_sql) || lang_sql

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/sql"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(sql.GetLanguage()), sql.GetQuery("highlights")
	}
	registerLang(fn, ".sql")
}
