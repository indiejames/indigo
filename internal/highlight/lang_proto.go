//go:build (lang_all && !lang_not_proto) || lang_proto

package highlight

import (
	"github.com/alexaandru/go-sitter-forest/proto"
	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(proto.GetLanguage()), proto.GetQuery("highlights")
	}
	registerLang(fn, ".proto")
}
