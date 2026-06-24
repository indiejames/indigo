//go:build (lang_all && !lang_not_proto) || lang_proto

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/proto"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(proto.GetLanguage()), proto.GetQuery("highlights")
	}
	registerLang(fn, ".proto")
}
