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

// RegisterAlias maps extension or filename key from to the same language as to.
// to can be a key with or without a leading dot (e.g. ".sh" or "sh").
// The line-comment prefix and any secondary parser are also inherited.
// Returns true if to resolves to a known language.
func RegisterAlias(from, to string) bool {
	fn, ok := langRegistry[to]
	if !ok {
		fn, ok = langRegistry["."+to]
		if !ok {
			return false
		}
		to = "." + to
	}
	langRegistry[from] = fn
	if extraFn, ok2 := extraLangRegistry[to]; ok2 {
		extraLangRegistry[from] = extraFn
	}
	if comment, ok2 := lineCommentByKey[to]; ok2 {
		lineCommentByKey[from] = comment
	}
	return true
}

// NewForKey creates a Highlighter for the given registry key (e.g. ".go", "dockerfile", "go").
// Returns nil if the key is not registered.
func NewForKey(key string) *Highlighter {
	fn, ok := langRegistry[key]
	if !ok {
		fn, ok = langRegistry["."+key]
		if !ok {
			return nil
		}
		key = "." + key
	}
	lang, qsrc := fn()
	if lang == nil || len(qsrc) == 0 {
		return nil
	}
	q, err := sitter.NewQuery(lang, qsrc)
	if err != nil {
		return nil
	}
	h := &Highlighter{lang: lang, query: q}
	if efn, ok2 := extraLangRegistry[key]; ok2 {
		elang, eqsrc := efn()
		if elang != nil && len(eqsrc) > 0 {
			if eq, err := sitter.NewQuery(elang, eqsrc); err == nil {
				h.extra = &Highlighter{lang: elang, query: eq}
			}
		}
	}
	return h
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
