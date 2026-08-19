// indigo-spell is a spell-checking plugin for the indigo editor.
//
// It decorates misspelled words with an underline (undercurl when the terminal
// supports it) and offers suggestions via the Shift+F fix popup.
// Words can be added to a global or workspace dictionary with the :spell-add
// and :spell-add-workspace ex commands.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "embed"

	"github.com/client9/gospell"
	"github.com/indiejames/indigo/sdk"
)

// en_US Hunspell dictionary — sourced from https://github.com/wooorm/dictionaries
// (MIT license, generated from SCOWL http://wordlist.aspell.net/)
//
//go:embed dict/en_US.aff
var affData []byte

//go:embed dict/en_US.dic
var dicData []byte

// Supplemental programming word lists — sourced from https://github.com/streetsidesoftware/cspell-dicts
// (MIT license)
//
//go:embed dict/software-terms.txt
var softwareTermsData []byte

//go:embed dict/coding-terms.txt
var codingTermsData []byte

//go:embed dict/computing-acronyms.txt
var computingAcronymsData []byte

// underlineStyle is set once at startup based on terminal capabilities.
var underlineStyle sdk.UnderlineStyle

func init() {
	// Detect undercurl support from TERM_PROGRAM (checked once at startup).
	switch os.Getenv("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm", "kitty", "ghostty":
		underlineStyle = sdk.UnderlineCurly
	default:
		underlineStyle = sdk.UnderlineStraight
	}
}

// fixPayload is the opaque token passed through FixData / GetFixes.
type fixPayload struct {
	Word string `json:"word"`
	Line uint32 `json:"line"`
	Col  uint32 `json:"col"`
}

// fileKind controls which portions of a file are spell-checked.
type fileKind int

const (
	kindText   fileKind = iota // check all text (markdown, plain text)
	kindCStyle                  // check only // and /* */ comments + string literals
	kindHash                    // check only # comments
	kindSkip                    // skip entirely (binary-like data files)
)

// fileKindForPath determines the checking strategy based on file extension.
func fileKindForPath(path string) fileKind {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".txt", ".rst", ".adoc", ".org", "":
		return kindText
	case ".json", ".lock", ".sum":
		return kindSkip
	case ".py", ".rb", ".sh", ".bash", ".zsh", ".fish", ".r",
		".toml", ".yaml", ".yml", ".ini", ".conf":
		return kindHash
	default:
		// Go, C, C++, JS, TS, Java, Rust, Swift, Kotlin, …
		return kindCStyle
	}
}

// commentSpan returns the checkable portion of a source line and its column
// offset within the original line. Returns ("", -1) when nothing on the line
// should be checked.
func commentSpan(line string, kind fileKind) (text string, col int) {
	switch kind {
	case kindText:
		return line, 0
	case kindSkip:
		return "", -1
	case kindCStyle:
		return cstyleCommentSpan(line)
	case kindHash:
		return hashCommentSpan(line)
	}
	return "", -1
}

func cstyleCommentSpan(line string) (string, int) {
	trimmed := strings.TrimLeft(line, " \t")
	leadWS := len(line) - len(trimmed)

	// Full-line comment: // text
	if strings.HasPrefix(trimmed, "//") {
		return trimmed[2:], leadWS + 2
	}
	// Start of block comment: /* text
	if strings.HasPrefix(trimmed, "/*") {
		t := trimmed[2:]
		if end := strings.Index(t, "*/"); end >= 0 {
			t = t[:end]
		}
		return t, leadWS + 2
	}
	// Interior of block comment: * text  (but not */)
	if strings.HasPrefix(trimmed, "*") && !strings.HasPrefix(trimmed, "*/") {
		return trimmed[1:], leadWS + 1
	}
	// Inline trailing comment: code // text
	if idx := strings.Index(line, "//"); idx >= 0 {
		return line[idx+2:], idx + 2
	}
	return "", -1
}

func hashCommentSpan(line string) (string, int) {
	idx := strings.Index(line, "#")
	if idx < 0 {
		return "", -1
	}
	// Skip shebangs on the first line.
	if idx == 0 && strings.HasPrefix(line, "#!") {
		return "", -1
	}
	return line[idx+1:], idx + 1
}

// Spell holds all plugin state.
type Spell struct {
	mu            sync.Mutex
	api           *sdk.Api
	checker       *gospell.GoSpell
	extraCheckers []*gospell.GoSpell // additional language dicts from ~/.config/indigo/spell/dicts/

	// per-buffer cache: bufID → list of decorations
	cache    map[uint32][]sdk.Decoration
	pending  map[uint32]*time.Timer // debounce timers
	bufPaths map[uint32]string      // bufID → file path (for kind detection)

	// user dictionaries (words added at runtime)
	globalDictPath    string
	workspaceDictPath string
	userWords         map[string]struct{} // in-memory set (global + workspace)
}

func (s *Spell) Init(api *sdk.Api) sdk.Info {
	s.api = api
	s.cache = make(map[uint32][]sdk.Decoration)
	s.pending = make(map[uint32]*time.Timer)
	s.bufPaths = make(map[uint32]string)
	s.userWords = make(map[string]struct{})

	// Load the base en_US dictionary from embedded bytes.
	checker, err := gospell.NewGoSpellReader(
		bytes.NewReader(affData),
		bytes.NewReader(dicData),
	)
	if err != nil {
		api.BroadcastMessage(fmt.Sprintf("indigo-spell: failed to load dictionary: %v", err)) //nolint:errcheck
		return sdk.Info{Name: "indigo-spell", Version: "0.1.0"}
	}
	s.checker = checker

	// Add supplemental programming word lists.
	for _, data := range [][]byte{softwareTermsData, codingTermsData, computingAcronymsData} {
		s.addWordList(data)
	}

	// Resolve user dictionary paths.
	if cfgDir, err := os.UserConfigDir(); err == nil {
		s.globalDictPath = filepath.Join(cfgDir, "indigo", "spell", "user.dic")
		os.MkdirAll(filepath.Dir(s.globalDictPath), 0o755) //nolint:errcheck
		// Load extra language dictionaries and word lists from user config dir.
		s.loadExtraDicts(filepath.Join(cfgDir, "indigo", "spell", "dicts"))
		s.loadExtraWordLists(filepath.Join(cfgDir, "indigo", "spell", "wordlists"))
	}
	// Workspace dict: look for .indigo/spell.dic relative to the working directory.
	if cwd, err := os.Getwd(); err == nil {
		s.workspaceDictPath = filepath.Join(cwd, ".indigo", "spell.dic")
	}

	// Load persisted user words.
	for _, path := range []string{s.globalDictPath, s.workspaceDictPath} {
		if path != "" {
			s.loadUserDictFile(path)
		}
	}

	// Register handlers.
	api.DecorationsFull(sdk.DecorationHandlers{ //nolint:errcheck
		GetDecorations: s.getDecorations,
		GetFixes:       s.getFixes,
		ApplyFix:       s.applyFix,
	})

	api.HandleBufferEvents(sdk.BufferHandlers{ //nolint:errcheck
		OnChange: s.onBufferChange,
		OnOpen:   s.onBufferChange,
		OnClose:  s.onBufferClose,
	})

	api.OnCommand("spell-add", s.cmdAddGlobal)              //nolint:errcheck
	api.OnCommand("spell-add-workspace", s.cmdAddWorkspace) //nolint:errcheck

	return sdk.Info{Name: "indigo-spell", Version: "0.1.0"}
}

// addWordList reads a word list (lines starting with '#' are comments) and
// adds each non-empty word to the spell checker.
func (s *Spell) addWordList(data []byte) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		// Some lines may have a leading '!' (forbidden word marker in cspell).
		if line[0] == '!' {
			continue
		}
		s.checker.AddWordRaw(line)
	}
}

// loadUserDictFile adds words from a plain-text file (one word per line).
func (s *Spell) loadUserDictFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		w := strings.TrimSpace(sc.Text())
		if w != "" && w[0] != '#' {
			s.addUserWord(w)
		}
	}
}

// addUserWord adds a word to the in-memory set and the spell checker.
func (s *Spell) addUserWord(word string) {
	lower := strings.ToLower(word)
	s.userWords[lower] = struct{}{}
	s.checker.AddWordRaw(word)
}

// persistWord appends a word to a dictionary file.
func persistWord(path, word string) error {
	if path == "" {
		return fmt.Errorf("no dictionary path configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	_, err = fmt.Fprintln(f, word)
	return err
}

// spell returns true if the word is correctly spelled.
// Checks the user word set, then the base checker, then any extra language checkers.
func (s *Spell) spell(word string) bool {
	lower := strings.ToLower(word)
	if _, ok := s.userWords[lower]; ok {
		return true
	}
	if s.checker.Spell(word) {
		return true
	}
	for _, ec := range s.extraCheckers {
		if ec.Spell(word) {
			return true
		}
	}
	return false
}

// loadExtraDicts loads Hunspell .aff/.dic pairs from dir into s.extraCheckers.
// Each .aff file must have a matching .dic file with the same base name.
func (s *Spell) loadExtraDicts(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".aff") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".aff")
		affBytes, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		dicBytes, err := os.ReadFile(filepath.Join(dir, base+".dic"))
		if err != nil {
			continue
		}
		g, err := gospell.NewGoSpellReader(bytes.NewReader(affBytes), bytes.NewReader(dicBytes))
		if err != nil {
			continue
		}
		s.extraCheckers = append(s.extraCheckers, g)
	}
}

// loadExtraWordLists loads .txt word-list files from dir (one word per line, # comments).
func (s *Spell) loadExtraWordLists(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		s.addWordList(data)
	}
}

// checkBuffer reads a buffer and returns its decoration list alongside the
// same misspellings as diagnostics (for the status bar/diagnostics popup),
// plus the buffer version content was read at — see publishDiagnostics.
func (s *Spell) checkBuffer(bufID uint32) (decors []sdk.Decoration, diags []sdk.Diagnostic, version uint64) {
	// Fetched before ReadBuffer so it's never newer than the content below;
	// worst case it's slightly stale, and PublishDiagnostics's own
	// version check on the server discards the result if so.
	_, _, _, _, version, err := s.api.BufferInfo(bufID)
	if err != nil {
		return nil, nil, 0
	}
	content, err := s.api.ReadBuffer(bufID)
	if err != nil {
		return nil, nil, 0
	}

	s.mu.Lock()
	path := s.bufPaths[bufID]
	s.mu.Unlock()

	kind := fileKindForPath(path)
	if kind == kindSkip {
		return nil, nil, version
	}

	lines := strings.Split(content, "\n")
	for lineIdx, line := range lines {
		text, colOff := commentSpan(line, kind)
		if text == "" {
			continue
		}
		words := splitIdentifiers(text)
		for _, w := range words {
			col := w.col + colOff
			if s.spell(w.text) {
				continue
			}
			endCol := uint32(col + len([]rune(w.text)))
			payload, _ := json.Marshal(fixPayload{
				Word: w.text,
				Line: uint32(lineIdx),
				Col:  uint32(col),
			})
			decors = append(decors, sdk.Decoration{
				Line:           uint32(lineIdx),
				Col:            uint32(col),
				EndCol:         endCol,
				Kind:           sdk.DecorationUnderline,
				UnderlineStyle: underlineStyle,
				UnderlineColor: "#80c8fb",
				Fixable:        true,
				FixData:        string(payload),
			})
			diags = append(diags, sdk.Diagnostic{
				Range: sdk.Range{
					Start: sdk.Position{Line: uint32(lineIdx), Col: uint32(col)},
					End:   sdk.Position{Line: uint32(lineIdx), Col: endCol},
				},
				Severity: sdk.SeverityInfo,
				Message:  fmt.Sprintf("Possible misspelling: %q", w.text),
			})
		}
	}
	return decors, diags, version
}

// scheduleCheck debounces re-checking a buffer by 500ms after a change.
func (s *Spell) scheduleCheck(bufID uint32) {
	s.mu.Lock()
	if t, ok := s.pending[bufID]; ok {
		t.Stop()
	}
	s.pending[bufID] = time.AfterFunc(500*time.Millisecond, func() {
		decors, diags, version := s.checkBuffer(bufID)
		s.mu.Lock()
		s.cache[bufID] = decors
		delete(s.pending, bufID)
		s.mu.Unlock()
		if version != 0 {
			s.api.PublishDiagnostics(bufID, version, diags)
		}
	})
	s.mu.Unlock()
}

// --- SDK callbacks ---

func (s *Spell) onBufferChange(bufID uint32, path string) {
	if path != "" {
		s.mu.Lock()
		s.bufPaths[bufID] = path
		s.mu.Unlock()
	}
	s.scheduleCheck(bufID)
}

func (s *Spell) onBufferClose(bufID uint32, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.pending[bufID]; ok {
		t.Stop()
		delete(s.pending, bufID)
	}
	delete(s.cache, bufID)
	delete(s.bufPaths, bufID)
}

func (s *Spell) getDecorations(bufID uint32, _ uint64, _ sdk.Range) []sdk.Decoration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cache[bufID]
}

func (s *Spell) getFixes(fixData string) []sdk.FixItem {
	var payload fixPayload
	if err := json.Unmarshal([]byte(fixData), &payload); err != nil {
		return nil
	}

	suggestions, err := s.checker.Suggest(payload.Word, 8)
	if err != nil || len(suggestions) == 0 {
		return []sdk.FixItem{
			{Label: fmt.Sprintf("Add \"%s\" to global dictionary", payload.Word), Replace: ""},
			{Label: fmt.Sprintf("Add \"%s\" to workspace dictionary", payload.Word), Replace: ""},
		}
	}

	var items []sdk.FixItem
	for _, sug := range suggestions {
		items = append(items, sdk.FixItem{
			Label:   sug.Word,
			Replace: sug.Word,
		})
	}
	// Add dictionary options at the bottom.
	items = append(items,
		sdk.FixItem{Label: fmt.Sprintf("Add \"%s\" to global dictionary", payload.Word)},
		sdk.FixItem{Label: fmt.Sprintf("Add \"%s\" to workspace dictionary", payload.Word)},
	)
	return items
}

func (s *Spell) applyFix(fixData string, index uint32) {
	var payload fixPayload
	if err := json.Unmarshal([]byte(fixData), &payload); err != nil {
		return
	}
	suggestions, _ := s.checker.Suggest(payload.Word, 8)
	// Index past suggestions = dictionary add actions.
	addGlobalIdx := uint32(len(suggestions))
	addWorkspaceIdx := addGlobalIdx + 1

	switch index {
	case addGlobalIdx:
		s.mu.Lock()
		s.addUserWord(payload.Word)
		s.mu.Unlock()
		persistWord(s.globalDictPath, payload.Word) //nolint:errcheck
		// Invalidate all cached results so the decoration disappears.
		s.invalidateAll()
	case addWorkspaceIdx:
		s.mu.Lock()
		s.addUserWord(payload.Word)
		s.mu.Unlock()
		persistWord(s.workspaceDictPath, payload.Word) //nolint:errcheck
		s.invalidateAll()
	}
}

func (s *Spell) invalidateAll() {
	s.mu.Lock()
	s.cache = make(map[uint32][]sdk.Decoration)
	s.mu.Unlock()
}

// --- Ex commands ---

func (s *Spell) cmdAddGlobal(args []string, _ sdk.CommandContext) {
	if len(args) == 0 {
		s.api.BroadcastMessage("Usage: :spell-add <word>") //nolint:errcheck
		return
	}
	word := args[0]
	s.mu.Lock()
	s.addUserWord(word)
	s.mu.Unlock()
	if err := persistWord(s.globalDictPath, word); err != nil {
		s.api.BroadcastMessage(fmt.Sprintf("spell-add: %v", err)) //nolint:errcheck
		return
	}
	s.invalidateAll()
	s.api.BroadcastMessage(fmt.Sprintf("Added \"%s\" to global dictionary", word)) //nolint:errcheck
}

func (s *Spell) cmdAddWorkspace(args []string, _ sdk.CommandContext) {
	if len(args) == 0 {
		s.api.BroadcastMessage("Usage: :spell-add-workspace <word>") //nolint:errcheck
		return
	}
	word := args[0]
	s.mu.Lock()
	s.addUserWord(word)
	s.mu.Unlock()
	if err := persistWord(s.workspaceDictPath, word); err != nil {
		s.api.BroadcastMessage(fmt.Sprintf("spell-add-workspace: %v", err)) //nolint:errcheck
		return
	}
	s.invalidateAll()
	s.api.BroadcastMessage(fmt.Sprintf("Added \"%s\" to workspace dictionary", word)) //nolint:errcheck
}

// --- Identifier splitting ---

// wordPos is a word extracted from a line with its starting rune column.
type wordPos struct {
	text string
	col  int
}

var urlRe = regexp.MustCompile(`https?://\S+`)
var hexRe = regexp.MustCompile(`^0[xX][0-9a-fA-F]+$`)

// splitIdentifiers breaks a source line into checkable words. It handles:
// - camelCase → ["camel", "Case"]
// - snake_case → ["snake", "case"]
// - ALLCAPS → skip (likely acronym or constant)
// - short words (≤2 chars) → skip
// - hex literals → skip
// - URLs → skip
func splitIdentifiers(line string) []wordPos {
	// Erase URLs so they don't contribute tokens.
	line = urlRe.ReplaceAllString(line, "")

	runes := []rune(line)
	var result []wordPos

	i := 0
	for i < len(runes) {
		// Skip non-letter characters.
		if !unicode.IsLetter(runes[i]) {
			i++
			continue
		}
		// Collect a contiguous letter+digit+underscore run (an identifier).
		// Allow apostrophes (straight ' or curly ') between letters so that
		// contractions like "doesn't" are treated as one token.
		start := i
		for i < len(runes) {
			r := runes[i]
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
				i++
				continue
			}
			// Apostrophe mid-word: allow only when followed by a letter.
			if (r == '\'' || r == '’') && i+1 < len(runes) && unicode.IsLetter(runes[i+1]) {
				i++
				continue
			}
			break
		}
		token := string(runes[start:i])

		// Strip a trailing apostrophe that slipped through (e.g. possessives: "James'").
		token = strings.TrimRight(token, "'’")

		// Split by underscore first.
		parts := strings.Split(token, "_")
		colOffset := start
		for _, part := range parts {
			if part == "" {
				colOffset++ // underscore
				continue
			}
			// Split by camelCase.
			subwords := splitCamel([]rune(part))
			for _, sub := range subwords {
				w := string(sub)
				extractWords(w, colOffset, &result)
				colOffset += len(sub)
			}
			colOffset++ // underscore separator
		}
	}
	return result
}

// extractWords checks a single "word" (after identifier splitting) and appends
// it to result if it passes all filters.
func extractWords(w string, col int, result *[]wordPos) {
	if len([]rune(w)) <= 2 {
		return // too short
	}
	if hexRe.MatchString(w) {
		return
	}
	// Skip all-caps (likely acronym).
	if isAllUpper(w) {
		return
	}
	// Skip words that contain digits (likely mixed identifier fragment like "v2").
	if containsDigit(w) {
		return
	}
	*result = append(*result, wordPos{text: w, col: col})
}

// splitCamel splits a rune slice on camelCase boundaries.
func splitCamel(runes []rune) [][]rune {
	if len(runes) == 0 {
		return nil
	}
	var parts [][]rune
	start := 0
	for i := 1; i < len(runes); i++ {
		if unicode.IsUpper(runes[i]) && unicode.IsLower(runes[i-1]) {
			parts = append(parts, runes[start:i])
			start = i
		} else if i+1 < len(runes) && unicode.IsUpper(runes[i]) && unicode.IsUpper(runes[i-1]) && unicode.IsLower(runes[i+1]) {
			// e.g. "HTMLParser" → ["HTML", "Parser"]
			parts = append(parts, runes[start:i])
			start = i
		}
	}
	parts = append(parts, runes[start:])
	return parts
}

func isAllUpper(s string) bool {
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
	}
	return true
}

func containsDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func main() {
	s := &Spell{}
	if err := sdk.Run(s); err != nil {
		fmt.Fprintf(os.Stderr, "indigo-spell: %v\n", err)
		os.Exit(1)
	}
}
