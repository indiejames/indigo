package highlight

import (
	"strings"

	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

var langRegistry = map[string]func() (*sitter.Language, []byte){}

func registerLang(fn func() (*sitter.Language, []byte), keys ...string) {
	for _, k := range keys {
		langRegistry[k] = fn
	}
}

func languageForPath(filePath string) (*sitter.Language, []byte) {
	base := filePath
	if i := strings.LastIndex(filePath, "/"); i >= 0 {
		base = filePath[i+1:]
	}
	if fn, ok := langRegistry[strings.ToLower(base)]; ok {
		return fn()
	}
	if i := strings.LastIndex(filePath, "."); i >= 0 {
		ext := strings.ToLower(filePath[i:])
		if fn, ok := langRegistry[ext]; ok {
			return fn()
		}
	}
	return nil, nil
}
