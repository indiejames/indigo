//go:build (lang_all && !lang_not_sql) || lang_sql

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/sql"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(sql.GetLanguage()), sql.GetQuery("highlights")
	}
	registerLang(fn, ".sql")
}
