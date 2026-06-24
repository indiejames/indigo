//go:build (lang_all && !lang_not_clojure) || lang_clojure

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/clojure"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(clojure.GetLanguage()), clojure.GetQuery("highlights")
	}
	registerLang(fn, ".clj", ".cljs", ".cljc", ".edn")
}
