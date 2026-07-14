//go:build (lang_all && !lang_not_gitcommit) || lang_gitcommit

package highlight

import (
	sitter "github.com/alexaandru/go-tree-sitter-bare"
	"github.com/alexaandru/go-sitter-forest/gitcommit"
)

func init() {
	fn := func() (*sitter.Language, []byte) {
		return sitter.NewLanguage(gitcommit.GetLanguage()), gitcommit.GetQuery("highlights")
	}
	// Git edit filenames (no extension; matched by base filename).
	registerLang(fn,
		"commit_editmsg",
		"merge_msg",
		"squash_msg",
		"tag_editmsg",
		"merge_head",
	)
	lineCommentByKey["commit_editmsg"] = "#"
	lineCommentByKey["merge_msg"] = "#"
	lineCommentByKey["squash_msg"] = "#"
	lineCommentByKey["tag_editmsg"] = "#"
}
