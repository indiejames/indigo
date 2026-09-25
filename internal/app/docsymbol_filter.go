package app

import "github.com/indiejames/indigo/internal/client"

// The document symbol picker's filters: which kinds of symbol to show, and
// whether to show only top-level ones.
//
// Kinds are grouped rather than offered one checkbox each: LSP defines 26
// symbol kinds, most of which no one would think to filter individually, and a
// row of 26 boxes is not a filter anyone would use.

// symCategory groups LSP SymbolKinds.
type symCategory int

const (
	symCatFunctions symCategory = iota
	symCatTypes
	symCatVariables
	symCatMembers
	symCatOther
	numSymCategories
)

var symCategoryLabels = [numSymCategories]string{"Functions", "Types", "Variables", "Members", "Other"}

// symbolCategory maps an LSP SymbolKind (spec numbering) to its category.
func symbolCategory(kind uint8) symCategory {
	switch kind {
	case 6, 9, 12: // Method, Constructor, Function
		return symCatFunctions
	case 2, 3, 4, 5, 10, 11, 23: // Module, Namespace, Package, Class, Enum, Interface, Struct
		return symCatTypes
	// Variable, Constant, and the value kinds some servers report for a
	// top-level literal: String, Number, Boolean, Array, Object, Null. These
	// are the maps and arrays defined at the top of a file.
	case 13, 14, 15, 16, 17, 18, 19, 21:
		return symCatVariables
	case 7, 8, 20, 22, 24: // Property, Field, Key, EnumMember, Event
		return symCatMembers
	default: // File, Operator, TypeParameter, and anything newer
		return symCatOther
	}
}

// docSymbolFilters is what the picker's checkboxes control.
type docSymbolFilters struct {
	// topLevelOnly hides symbols nested in another — a method in a class, a
	// variable in a function, a property of a top-level object. "Top-level"
	// is an empty ContainerName, which lsp.Client.DocumentSymbols guarantees
	// for exactly the roots of the server's symbol tree.
	topLevelOnly bool
	show         [numSymCategories]bool
}

// defaultDocSymbolFilters shows top-level functions, types and variables: a
// file's own declarations — including top-level maps and arrays — without its
// methods, fields and locals.
func defaultDocSymbolFilters() docSymbolFilters {
	f := docSymbolFilters{topLevelOnly: true}
	f.show[symCatFunctions] = true
	f.show[symCatTypes] = true
	f.show[symCatVariables] = true
	return f
}

// allows reports whether s passes the checkboxes.
func (f docSymbolFilters) allows(s client.ClientSymbol) bool {
	if f.topLevelOnly && s.ContainerName != "" {
		return false
	}
	return f.show[symbolCategory(s.Kind)]
}

// Checkbox focus positions in the picker. 0 is the text filter; the boxes
// follow in display order.
const (
	docSymFocusFilter   = 0
	docSymFocusTopLevel = 1
	// docSymFocusCategory0 + c is category c's box.
	docSymFocusCategory0 = 2
	docSymFocusCount     = docSymFocusCategory0 + int(numSymCategories)
)

// toggle flips the checkbox at focus position pos. A no-op for the text
// filter's position.
func (f *docSymbolFilters) toggle(pos int) {
	switch {
	case pos == docSymFocusTopLevel:
		f.topLevelOnly = !f.topLevelOnly
	case pos >= docSymFocusCategory0 && pos < docSymFocusCount:
		c := pos - docSymFocusCategory0
		f.show[c] = !f.show[c]
	}
}
