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
	"unicode/utf8"

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
	kindCStyle                 // check only // and /* */ comments + string literals
	kindHash                   // check only # comments
	kindSkip                   // skip entirely (binary-like data files)
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

	// per-buffer incremental-check state: lastLines is the buffer content at
	// the last completed check, split into lines, used to diff against the
	// next check's content so only lines that actually changed get
	// re-spell-checked; lineDecors/lineDiags hold that check's results keyed
	// by line number so unchanged lines' results can be carried forward
	// (shifted by any line-count delta) instead of recomputed. See
	// checkBuffer's doc comment.
	lastLines  map[uint32][]string
	lineDecors map[uint32]map[uint32][]sdk.Decoration
	lineDiags  map[uint32]map[uint32][]sdk.Diagnostic
	// generation guards against a superseded check clobbering a newer one's
	// result: scheduleCheck bumps it and each check's callback captures the
	// value at schedule time, only applying its result if the buffer's
	// generation hasn't moved on by the time it completes. Needed because
	// t.Stop() on an already-fired timer is a no-op — if invalidateAll (or
	// another edit) reschedules a check while an older one for the same
	// buffer is mid-flight (its own two RPC round trips: BufferInfo then
	// ReadBuffer), both run concurrently with no guarantee the newer one
	// finishes last.
	generation map[uint32]uint64

	// user dictionaries (words added at runtime)
	globalDictPath    string
	workspaceDictPath string
	userWords         map[string]struct{} // in-memory set (global + workspace)

	// workspace-scan state, guarded by mu. The editor dispatches
	// OnWorkspaceScan fire-and-forget on every trigger (startup, an
	// explicit rescan) with no coalescing of its own, so two triggers close
	// together would otherwise walk the tree concurrently — scanning +
	// rescanPending serialize runs the same way lint.Manager.ScanWorkspace
	// coalesces concurrent requests on the editor side: a request that
	// arrives mid-scan doesn't spawn a second walk, it just schedules one
	// more run after the current one finishes. scannedPaths remembers which
	// files had diagnostics published by the last completed scan, so the
	// next one can explicitly clear (publish empty for) any that no longer
	// do — a file that's fixed, deleted, or renamed between scans would
	// otherwise keep a stale diagnostic forever, since nothing else ever
	// revisits it.
	scanning      bool
	rescanPending bool
	scannedPaths  map[string]struct{}
}

func (s *Spell) Init(api *sdk.Api) sdk.Info {
	s.api = api
	s.cache = make(map[uint32][]sdk.Decoration)
	s.pending = make(map[uint32]*time.Timer)
	s.generation = make(map[uint32]uint64)
	s.bufPaths = make(map[uint32]string)
	s.lastLines = make(map[uint32][]string)
	s.lineDecors = make(map[uint32]map[uint32][]sdk.Decoration)
	s.lineDiags = make(map[uint32]map[uint32][]sdk.Diagnostic)
	s.userWords = make(map[string]struct{})
	s.scannedPaths = make(map[string]struct{})

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

	api.OnWorkspaceScan(func() { go s.runWorkspaceScan() }) //nolint:errcheck

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
//
// s.checker.Spell reads gospell's underlying dict, a plain map with no
// internal synchronization of its own — it must be held under s.mu, the same
// lock addUserWord's s.checker.AddWordRaw already writes under, or a
// concurrent add-to-dictionary (applyFix/cmdAddGlobal/cmdAddWorkspace) racing
// a spell check crashes the whole plugin process with Go's fatal "concurrent
// map read and map write".
func (s *Spell) spell(word string) bool {
	lower := strings.ToLower(word)
	s.mu.Lock()
	_, known := s.userWords[lower]
	if !known {
		known = s.checker.Spell(word)
	}
	s.mu.Unlock()
	if known {
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

// misspelling is one flagged word's position and text, shared between the
// per-buffer (checkBuffer) and whole-project (checkFileDiagnostics) checking
// paths so the identifier-splitting/comment-scoping logic below is written
// once.
type misspelling struct {
	line, col, endCol uint32
	word              string
}

// findMisspellings scans content (the text of the file at path, whichever
// source it came from — a live buffer or disk) and returns every word that
// fails s.spell, restricted to the checkable portion of each line per
// fileKindForPath/commentSpan (comments only for source files, everything
// for text files, nothing for kindSkip).
func (s *Spell) findMisspellings(path, content string) []misspelling {
	kind := fileKindForPath(path)
	if kind == kindSkip {
		return nil
	}
	var out []misspelling
	for lineIdx, line := range strings.Split(content, "\n") {
		out = append(out, s.checkLineMisspellings(path, kind, line, uint32(lineIdx))...)
	}
	return out
}

// checkLineMisspellings is findMisspellings' single-line counterpart, shared
// by the whole-file path above and checkBuffer's incremental per-line
// rechecks below.
func (s *Spell) checkLineMisspellings(path string, kind fileKind, line string, lineIdx uint32) []misspelling {
	text, colOff := commentSpan(line, kind)
	if text == "" {
		return nil
	}
	// colOff is a byte offset into line (from len()/strings.Index); w.col
	// below is a rune offset into text. Convert before combining them, or
	// any multi-byte rune earlier on the line throws every column after
	// it off for both the decoration and the diagnostic built from it.
	colOffRunes := utf8.RuneCountInString(line[:colOff])
	var out []misspelling
	for _, w := range splitIdentifiers(text) {
		col := w.col + colOffRunes
		if s.spell(w.text) {
			continue
		}
		endCol := uint32(col + len([]rune(w.text)))
		out = append(out, misspelling{line: lineIdx, col: uint32(col), endCol: endCol, word: w.text})
	}
	return out
}

// misspellingsToDecors/misspellingsToDiags convert a line's misspellings
// into the two published forms — split out so checkBuffer's per-line
// incremental recheck and the whole-file paths build identical results.
func misspellingsToDecors(ms []misspelling) []sdk.Decoration {
	var decors []sdk.Decoration
	for _, m := range ms {
		payload, _ := json.Marshal(fixPayload{Word: m.word, Line: m.line, Col: m.col})
		decors = append(decors, sdk.Decoration{
			Line:           m.line,
			Col:            m.col,
			EndCol:         m.endCol,
			Kind:           sdk.DecorationUnderline,
			UnderlineStyle: underlineStyle,
			UnderlineColor: "#80c8fb",
			Fixable:        true,
			FixData:        string(payload),
		})
	}
	return decors
}

func misspellingsToDiags(ms []misspelling) []sdk.Diagnostic {
	var diags []sdk.Diagnostic
	for _, m := range ms {
		diags = append(diags, sdk.Diagnostic{
			Range: sdk.Range{
				Start: sdk.Position{Line: m.line, Col: m.col},
				End:   sdk.Position{Line: m.line, Col: m.endCol},
			},
			Severity: sdk.SeverityInfo,
			Message:  fmt.Sprintf("Possible misspelling: %q", m.word),
		})
	}
	return diags
}

// flattenDecors/flattenDiags collapse a per-line result map (as produced and
// stored by checkBuffer/applyCheckResult) into the flat slices GetDecorations
// and PublishDiagnostics expect.
func flattenDecors(m map[uint32][]sdk.Decoration) []sdk.Decoration {
	var out []sdk.Decoration
	for _, d := range m {
		out = append(out, d...)
	}
	return out
}

func flattenDiags(m map[uint32][]sdk.Diagnostic) []sdk.Diagnostic {
	var out []sdk.Diagnostic
	for _, d := range m {
		out = append(out, d...)
	}
	return out
}

// commonPrefixLen returns the number of leading elements identical between
// a and b.
func commonPrefixLen(a, b []string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// commonSuffixLen returns the number of trailing elements identical between
// a and b, not overlapping the first `prefix` elements of either slice —
// prefix must be commonPrefixLen(a, b) (or smaller) so the two spans can
// never overlap on a slice that's a prefix of itself.
func commonSuffixLen(a, b []string, prefix int) int {
	i, j := len(a), len(b)
	n := 0
	for i > prefix && j > prefix && a[i-1] == b[j-1] {
		i--
		j--
		n++
	}
	return n
}

// checkBuffer reads a buffer and returns its new content split into lines
// (the next check's diff baseline) plus its decorations/diagnostics, each
// keyed by line number, and the buffer version content was read at — see
// publishDiagnostics. ok is false only on an actual read failure; distinct
// from version, since a freshly opened, not-yet-edited buffer legitimately
// has version 0.
//
// Only lines that actually changed since the last check (diffed against the
// per-buffer baseline in s.lastLines via a common-prefix/common-suffix
// comparison) are re-spell-checked; every other line's result is carried
// over from s.lineDecors/s.lineDiags, shifted by the line-count delta if the
// edit added or removed lines. This turns a recheck after typing a single
// character into O(1) spell-check work instead of O(file size) — the common
// case, since scheduleCheck fires on every buffer change.
func (s *Spell) checkBuffer(bufID uint32) (lines []string, lineDecors map[uint32][]sdk.Decoration, lineDiags map[uint32][]sdk.Diagnostic, version uint64, ok bool) {
	// Fetched before ReadBuffer so it's never newer than the content below;
	// worst case it's slightly stale, and PublishDiagnostics's own
	// version check on the server discards the result if so.
	_, _, _, _, version, err := s.api.BufferInfo(bufID)
	if err != nil {
		return nil, nil, nil, 0, false
	}
	content, err := s.api.ReadBuffer(bufID)
	if err != nil {
		return nil, nil, nil, 0, false
	}

	s.mu.Lock()
	path := s.bufPaths[bufID]
	oldLines := s.lastLines[bufID]
	oldLineDecors := s.lineDecors[bufID]
	oldLineDiags := s.lineDiags[bufID]
	s.mu.Unlock()

	newLines := strings.Split(content, "\n")
	lineDecors, lineDiags = s.diffAndCheck(path, oldLines, newLines, oldLineDecors, oldLineDiags)
	return newLines, lineDecors, lineDiags, version, true
}

// diffAndCheck is checkBuffer's diff-and-recheck core, split out so it's
// testable without a live sdk.Api connection (it touches only s.checker/
// s.userWords via s.spell, not s.api). Given the previous check's line-keyed
// results and the previous/new buffer content, it recomputes spell-check
// results only for the lines that actually changed (a common-prefix/
// common-suffix comparison against oldLines) and carries every other line's
// cached result forward, shifted by the line-count delta if the edit added
// or removed lines.
func (s *Spell) diffAndCheck(path string, oldLines, newLines []string, oldLineDecors map[uint32][]sdk.Decoration, oldLineDiags map[uint32][]sdk.Diagnostic) (lineDecors map[uint32][]sdk.Decoration, lineDiags map[uint32][]sdk.Diagnostic) {
	kind := fileKindForPath(path)

	prefix := commonPrefixLen(oldLines, newLines)
	suffix := commonSuffixLen(oldLines, newLines, prefix)
	changeEndNew := len(newLines) - suffix
	if changeEndNew < prefix {
		changeEndNew = prefix
	}
	oldChangeEnd := len(oldLines) - suffix
	delta := len(newLines) - len(oldLines)

	lineDecors = make(map[uint32][]sdk.Decoration, len(oldLineDecors))
	lineDiags = make(map[uint32][]sdk.Diagnostic, len(oldLineDiags))

	// Unchanged leading lines carry over unshifted.
	for line := 0; line < prefix; line++ {
		l := uint32(line)
		if d, ok := oldLineDecors[l]; ok {
			lineDecors[l] = d
		}
		if d, ok := oldLineDiags[l]; ok {
			lineDiags[l] = d
		}
	}
	// Unchanged trailing lines carry over shifted by however many lines the
	// edit added/removed in the middle.
	for line := oldChangeEnd; line < len(oldLines); line++ {
		l := uint32(line + delta)
		if d, ok := oldLineDecors[uint32(line)]; ok {
			lineDecors[l] = d
		}
		if d, ok := oldLineDiags[uint32(line)]; ok {
			lineDiags[l] = d
		}
	}
	// Only the changed middle range is actually re-spell-checked.
	if kind != kindSkip {
		for line := prefix; line < changeEndNew; line++ {
			l := uint32(line)
			ms := s.checkLineMisspellings(path, kind, newLines[line], l)
			if len(ms) > 0 {
				lineDecors[l] = misspellingsToDecors(ms)
				lineDiags[l] = misspellingsToDiags(ms)
			}
		}
	}

	return lineDecors, lineDiags
}

// checkFileDiagnostics is checkBuffer's counterpart for a file read from
// disk rather than a live buffer — used by the workspace scan, where no
// bufID/decorations exist, only a path and its on-disk content.
func (s *Spell) checkFileDiagnostics(path, content string) []sdk.Diagnostic {
	var diags []sdk.Diagnostic
	for _, m := range s.findMisspellings(path, content) {
		diags = append(diags, sdk.Diagnostic{
			Range: sdk.Range{
				Start: sdk.Position{Line: m.line, Col: m.col},
				End:   sdk.Position{Line: m.line, Col: m.endCol},
			},
			Severity: sdk.SeverityInfo,
			Message:  fmt.Sprintf("Possible misspelling: %q", m.word),
		})
	}
	return diags
}

// maxScanFileSize skips files larger than this during a workspace scan, so
// a stray large generated/data file that fileKindForPath's extension list
// doesn't already exclude can't make a scan spend a long time tokenizing it.
const maxScanFileSize = 1 << 20 // 1MB

// skipScanDirs are directory names never descended into during a workspace
// scan: VCS metadata and dependency/build output, never source the user is
// actively writing prose or comments into. Dot-directories (.git among them)
// are skipped generically below; this covers the common non-dot ones.
var skipScanDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"target":       true,
}

// runWorkspaceScan walks every file under the plugin process's working
// directory (the workspace root — see Init's s.workspaceDictPath comment,
// which relies on the same assumption) and publishes spelling diagnostics
// for each one via PublishWorkspaceDiagnostics. Registered as the
// OnWorkspaceScan handler; the editor calls it at startup and on an
// explicit rescan.
func (s *Spell) runWorkspaceScan() {
	s.runScanSerialized(s.scanWorkspaceOnce)
}

// runScanSerialized guards against overlapping scanOnce runs — the editor
// dispatches OnWorkspaceScan fire-and-forget on every trigger (startup, an
// explicit rescan) with no coalescing on its own side, so two triggers
// close together would otherwise walk the tree concurrently and could
// publish an older run's result for a path after a newer run's. If a call
// arrives while one is already in flight, it doesn't spawn a second run —
// it just schedules exactly one more run after the current one finishes
// (however many overlapping requests arrive meanwhile, they coalesce into
// that single rerun), mirroring lint.Manager.ScanWorkspace's own
// concurrent-request coalescing on the editor side.
func (s *Spell) runScanSerialized(scanOnce func()) {
	s.mu.Lock()
	if s.scanning {
		s.rescanPending = true
		s.mu.Unlock()
		return
	}
	s.scanning = true
	s.mu.Unlock()

	for {
		scanOnce()

		s.mu.Lock()
		if !s.rescanPending {
			s.scanning = false
			s.mu.Unlock()
			return
		}
		s.rescanPending = false
		s.mu.Unlock()
	}
}

// scanWorkspaceOnce is one walk of the workspace tree, called only from
// runWorkspaceScan (which guarantees at most one of these runs at a time).
// It publishes diagnostics for every file with misspellings, then clears
// (publishes empty for) any path that had diagnostics after the previous
// scan but not this one — fixed, deleted, or renamed since — so a stale
// diagnostic never lingers past a file actually being clean or gone.
func (s *Spell) scanWorkspaceOnce() {
	root, err := os.Getwd()
	if err != nil {
		return
	}
	found := make(map[string]struct{})
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error { //nolint:errcheck
		if err != nil {
			return nil // unreadable entry (permissions, race with deletion) — skip, keep walking
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || skipScanDirs[name]) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxScanFileSize {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || !utf8.Valid(data) {
			return nil
		}
		diags := s.checkFileDiagnostics(path, string(data))
		if len(diags) == 0 {
			return nil // nothing to publish; cleared below if it had diags last scan
		}
		found[path] = struct{}{}
		s.api.PublishWorkspaceDiagnostics(path, diags)
		return nil
	})

	s.mu.Lock()
	prev := s.scannedPaths
	s.scannedPaths = found
	s.mu.Unlock()
	for _, path := range pathsToClear(prev, found) {
		s.api.PublishWorkspaceDiagnostics(path, nil)
	}
}

// pathsToClear returns every path in prev that's absent from found — the
// diff that drives which previously-published paths need an explicit empty
// publish to clear a now-stale diagnostic (fixed, deleted, or renamed since
// the scan that produced prev).
func pathsToClear(prev, found map[string]struct{}) []string {
	var out []string
	for path := range prev {
		if _, ok := found[path]; !ok {
			out = append(out, path)
		}
	}
	return out
}

// checkDebounce is how long scheduleCheck waits after the last buffer change
// before actually rechecking it.
const checkDebounce = 250 * time.Millisecond

// scheduleCheck debounces re-checking a buffer by checkDebounce after a change.
func (s *Spell) scheduleCheck(bufID uint32) {
	s.mu.Lock()
	if t, ok := s.pending[bufID]; ok {
		t.Stop()
	}
	s.generation[bufID]++
	gen := s.generation[bufID]
	s.pending[bufID] = time.AfterFunc(checkDebounce, func() {
		lines, lineDecors, lineDiags, version, ok := s.checkBuffer(bufID)
		s.applyCheckResult(bufID, gen, lines, lineDecors, lineDiags, version, ok)
	})
	s.mu.Unlock()
}

// applyCheckResult stores a completed check's per-line results for bufID —
// both the flat cache/publish forms and the line-keyed incremental-diff
// baseline (lines/lineDecors/lineDiags) checkBuffer's next run will diff
// against — unless gen has been superseded by a newer scheduleCheck call
// since this check started. See the generation field's doc comment: a
// superseded result's diff baseline is itself unreliable (it was computed
// against content a newer check has already moved past), so it must be
// discarded exactly like the flat cache write always was.
func (s *Spell) applyCheckResult(bufID uint32, gen uint64, lines []string, lineDecors map[uint32][]sdk.Decoration, lineDiags map[uint32][]sdk.Diagnostic, version uint64, ok bool) {
	s.mu.Lock()
	delete(s.pending, bufID)
	stale := s.generation[bufID] != gen
	var diags []sdk.Diagnostic
	if !stale {
		s.cache[bufID] = flattenDecors(lineDecors)
		s.lastLines[bufID] = lines
		s.lineDecors[bufID] = lineDecors
		s.lineDiags[bufID] = lineDiags
		diags = flattenDiags(lineDiags)
	}
	s.mu.Unlock()
	if ok && !stale {
		s.api.PublishDiagnostics(bufID, version, diags)
		// Push the new decorations immediately rather than waiting for the
		// client's next poll tick (up to ~360ms) on top of the debounce
		// above — the two delays otherwise stack into a noticeable lag.
		s.api.RefreshDecorations(bufID)
	}
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
	delete(s.generation, bufID)
	delete(s.lastLines, bufID)
	delete(s.lineDecors, bufID)
	delete(s.lineDiags, bufID)
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

	// Held under s.mu — see spell()'s doc comment: Suggest reads the same
	// unsynchronized gospell dict that addUserWord's AddWordRaw writes to.
	s.mu.Lock()
	suggestions, err := s.checker.Suggest(payload.Word, 8)
	s.mu.Unlock()
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
	// Held under s.mu — see spell()'s doc comment: Suggest reads the same
	// unsynchronized gospell dict that addUserWord's AddWordRaw writes to.
	s.mu.Lock()
	suggestions, _ := s.checker.Suggest(payload.Word, 8)
	s.mu.Unlock()
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

// invalidateAll forces a fresh check of every buffer this plugin has seen
// opened, rather than merely blanking the decoration cache. Blanking alone
// left two things stale after a dictionary-add fix: the decoration cache
// would stay empty until the next actual edit to that specific buffer (which
// might never come), and — worse — the diagnostic already published for the
// fixed word via PublishDiagnostics was never cleared or replaced at all,
// since nothing re-ran checkBuffer to republish. A forced recheck fixes both:
// scheduleCheck's callback always calls PublishDiagnostics when it succeeds,
// including with an empty list, which is what actually clears a stale
// server-side diagnostic (see PluginPublishDiagnostics's empty-clears case).
func (s *Spell) invalidateAll() {
	s.mu.Lock()
	s.cache = make(map[uint32][]sdk.Decoration)
	// Clear the incremental-diff baseline too — a dictionary add changes
	// which words fail s.spell without changing any line's text, so a
	// same-content diff against the old baseline would (correctly, but
	// unhelpfully) find zero changed lines and carry every stale decoration
	// straight through. Dropping the baseline forces checkBuffer to treat
	// every line as changed on the next check.
	s.lastLines = make(map[uint32][]string)
	s.lineDecors = make(map[uint32]map[uint32][]sdk.Decoration)
	s.lineDiags = make(map[uint32]map[uint32][]sdk.Diagnostic)
	bufIDs := make([]uint32, 0, len(s.bufPaths))
	for id := range s.bufPaths {
		bufIDs = append(bufIDs, id)
	}
	s.mu.Unlock()
	for _, id := range bufIDs {
		s.scheduleCheck(id)
	}
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
