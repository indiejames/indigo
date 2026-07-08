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

// lineCommentByKey maps the registry lookup key (extension or filename) to the
// line comment prefix for that language.
var lineCommentByKey = map[string]string{
	// // style
	".go": "//", ".js": "//", ".mjs": "//", ".cjs": "//",
	".ts": "//", ".tsx": "//", ".java": "//", ".c": "//", ".h": "//",
	".cc": "//", ".cpp": "//", ".cxx": "//", ".hh": "//", ".hpp": "//", ".hxx": "//",
	".cs": "//", ".dart": "//", ".kt": "//", ".kts": "//",
	".groovy": "//", ".gvy": "//", ".gy": "//", ".gsh": "//",
	".scala": "//", ".sc": "//", ".php": "//", ".proto": "//",
	".tf": "//", ".hcl": "//", ".rs": "//", ".swift": "//",
	".svelte": "//",
	// # style
	".py": "#", ".rb": "#", ".sh": "#", ".bash": "#",
	"dockerfile": "#", ".r": "#", ".gd": "#",
	".ex": "#", ".exs": "#", ".nix": "#",
	".toml": "#", ".yaml": "#", ".yml": "#", ".graphql": "#", ".gql": "#",
	// -- style
	".lua": "--", ".hs": "--", ".lhs": "--", ".elm": "--",
	".sql": "--", ".ml": "--", ".mli": "--",
}

// LineCommentPrefix returns the line comment prefix for the given file path
// (e.g. "//" for Go, "#" for Python, "--" for Lua). Falls back to "//".
func LineCommentPrefix(filePath string) string {
	k := lookupKey(filePath)
	if p, ok := lineCommentByKey[k]; ok {
		return p
	}
	return "//"
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
