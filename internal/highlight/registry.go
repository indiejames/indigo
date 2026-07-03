package highlight

import (
	"strings"

	sitter "github.com/alexaandru/go-tree-sitter-bare"
)

var langRegistry = map[string]func() (*sitter.Language, []byte){}

// extraLangRegistry holds an optional secondary parser for an extension.
// Used by markdown to also run markdown_inline on the same content.
var extraLangRegistry = map[string]func() (*sitter.Language, []byte){}

func registerLang(fn func() (*sitter.Language, []byte), keys ...string) {
	for _, k := range keys {
		langRegistry[k] = fn
	}
}

// registerExtraLang attaches a secondary parser to the given extensions.
// It must be called after the primary parser is already registered.
func registerExtraLang(fn func() (*sitter.Language, []byte), keys ...string) {
	for _, k := range keys {
		extraLangRegistry[k] = fn
	}
}

func lookupKey(filePath string) string {
	base := filePath
	if i := strings.LastIndex(filePath, "/"); i >= 0 {
		base = filePath[i+1:]
	}
	if _, ok := langRegistry[strings.ToLower(base)]; ok {
		return strings.ToLower(base)
	}
	if i := strings.LastIndex(filePath, "."); i >= 0 {
		ext := strings.ToLower(filePath[i:])
		if _, ok := langRegistry[ext]; ok {
			return ext
		}
	}
	return ""
}

func languageForPath(filePath string) (*sitter.Language, []byte) {
	k := lookupKey(filePath)
	if k == "" {
		return nil, nil
	}
	return langRegistry[k]()
}

func extraLanguageForPath(filePath string) (*sitter.Language, []byte) {
	k := lookupKey(filePath)
	if k == "" {
		return nil, nil
	}
	fn, ok := extraLangRegistry[k]
	if !ok {
		return nil, nil
	}
	return fn()
}
