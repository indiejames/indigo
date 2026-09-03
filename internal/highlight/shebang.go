package highlight

import (
	"path"
	"strings"
)

// ShebangKey inspects the first line of content for a "#!" shebang and
// returns the registry key for the interpreter it names (e.g. "sh" for
// both "#!/bin/sh" and "#!/usr/bin/env bash"), or "" if there's no
// shebang or it names an interpreter this package doesn't map to a known
// language. content may be a whole file's content or just its first line
// — only the text up to the first newline is ever inspected.
//
// Used as a fallback when a file's extension alone doesn't resolve to a
// highlighter (see New) — most commonly an extension-less script, but
// also any file whose extension indigo doesn't otherwise recognize.
func ShebangKey(content string) string {
	line := content
	if i := strings.IndexByte(content, '\n'); i >= 0 {
		line = content[:i]
	}
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, "#!") {
		return ""
	}
	fields := strings.Fields(line[2:])
	if len(fields) == 0 {
		return ""
	}
	interp := path.Base(fields[0])
	if interp == "env" {
		// "#!/usr/bin/env [-S] <interpreter> [args...]" — skip env's own
		// flags (e.g. -S, used to pass the interpreter multiple arguments)
		// to find the actual interpreter name.
		interp = ""
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") {
				continue
			}
			interp = path.Base(f)
			break
		}
	}
	return shebangInterpreterKey(interp)
}

// shebangInterpreterKey maps an interpreter's basename (named directly, or
// via env) to the registry key for its language. Deliberately limited to
// interpreters indigo has a grammar for. A trailing version number
// ("python3", "php8") is matched via prefix rather than requiring an
// exhaustive list of every version.
func shebangInterpreterKey(name string) string {
	switch name := strings.ToLower(name); {
	case name == "sh", name == "bash", name == "dash", name == "ksh", name == "zsh":
		return "sh"
	case strings.HasPrefix(name, "python"):
		return "py"
	case name == "node", name == "nodejs":
		return "js"
	case name == "ruby":
		return "rb"
	case strings.HasPrefix(name, "php"):
		return "php"
	case name == "lua":
		return "lua"
	case name == "fish":
		return "fish"
	case name == "rscript":
		return "r"
	default:
		return ""
	}
}
