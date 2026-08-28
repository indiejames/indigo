package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	"github.com/indiejames/indigo/internal/procutil"
)

const maxGrepResults = 500

// maxFileBytes is a var, not a const, so tests can shrink it instead of
// generating multi-megabyte fixtures to exercise the oversized-file/record
// skip paths in searchBuiltin and searchWithRg.
var maxFileBytes = 5 * 1024 * 1024 // 5 MB

// GrepResult is one match found during workspace search.
type GrepResult struct {
	RelPath  string // workspace-relative path
	Line     int    // 0-based line number
	Col      int    // rune column of match start
	MatchLen int    // rune length of match
	LineText string // full content of the matching line (trimmed for display)
}

// searchWorkspace searches the workspace for pattern. It delegates to ripgrep
// when rg is available on PATH for best performance, and falls back to the
// built-in Go walker otherwise. include/exclude optionally restrict which
// files are searched — each is a comma- or whitespace-separated list of glob
// patterns (e.g. "*.go, src/**/*.ts"); empty string means no filter.
func searchWorkspace(workDir, pattern, include, exclude string) ([]GrepResult, error) {
	expr, isRegex := grepRegexExpr(pattern)
	if isRegex {
		pattern = expr
	}
	caseSensitive := isRegex || grepSmartCase(pattern)
	return searchWorkspaceExplicit(workDir, pattern, include, exclude, caseSensitive, isRegex)
}

// searchWorkspaceExplicit is searchWorkspace with regex-ness and
// case-sensitivity passed explicitly rather than inferred from a \pattern\
// prefix convention — used by the search/replace dialog's checkboxes, where
// the user sets those independently of what they type.
func searchWorkspaceExplicit(workDir, pattern, include, exclude string, caseSensitive, isRegex bool) ([]GrepResult, error) {
	if rgAvailable() {
		return searchWithRg(workDir, pattern, include, exclude, caseSensitive, isRegex)
	}
	return searchBuiltin(workDir, pattern, include, exclude, caseSensitive, isRegex)
}

// splitGlobs splits a comma- and/or whitespace-separated list of glob
// patterns into its individual, trimmed elements. Empty elements are
// dropped, so both "*.go,*.ts" and "*.go *.ts" (and combinations) work.
func splitGlobs(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
}

// matchGlobs reports whether relPath passes the include/exclude filters: it
// is rejected if it matches any exclude pattern, and otherwise accepted if
// there are no include patterns or it matches at least one of them.
func matchGlobs(includes, excludes []string, relPath string) bool {
	for _, ex := range excludes {
		if matchGlob(ex, relPath) {
			return false
		}
	}
	if len(includes) == 0 {
		return true
	}
	for _, in := range includes {
		if matchGlob(in, relPath) {
			return true
		}
	}
	return false
}

// rgExecutable is the rg binary name/path used by searchWithRg — a seam so
// tests can substitute a stand-in script without needing a real ripgrep
// binary with controllable output/timing.
var rgExecutable = "rg"

// rgAvailable reports whether rg is on the PATH.
func rgAvailable() bool {
	_, err := exec.LookPath(rgExecutable)
	return err == nil
}

// ---- ripgrep backend ----

// Minimal JSON types for ripgrep's --json output format.
type rgMsg struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type rgMatchData struct {
	Path       rgText       `json:"path"`
	Lines      rgText       `json:"lines"`
	LineNumber uint32       `json:"line_number"`
	Submatches []rgSubmatch `json:"submatches"`
}

type rgText struct {
	Text string `json:"text"`
}

type rgSubmatch struct {
	Start int `json:"start"` // byte offset within the line
	End   int `json:"end"`
}

// readCappedLine reads one '\n'-terminated record from r using
// bufio.Reader.ReadSlice, which — unlike bufio.Scanner or
// bufio.Reader.ReadBytes/ReadString — never grows its own buffer past a
// fixed size to accumulate an arbitrarily long token. That means a record
// longer than maxLen is detected (and its still-unread remainder drained,
// so the next call resumes at the following record rather than getting
// stuck on this one) using only the reader's fixed internal buffer, not
// memory proportional to the oversized record's actual length.
//
// Returns (nil, true, nil) for a record longer than maxLen, (line, false,
// nil) for a normal record, (nil, false, io.EOF) at a clean end of input
// with no more data, or (nil, false, err) on a genuine read error.
func readCappedLine(r *bufio.Reader, maxLen int) (line []byte, oversized bool, err error) {
	var buf []byte
	for {
		chunk, e := r.ReadSlice('\n')
		if !oversized && len(buf)+len(chunk) > maxLen {
			oversized = true
			buf = nil
		}
		if !oversized {
			buf = append(buf, chunk...)
		}
		switch e {
		case nil:
			if oversized {
				return nil, true, nil
			}
			return buf, false, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(buf) == 0 && !oversized {
				return nil, false, io.EOF
			}
			return buf, oversized, nil
		default:
			return nil, false, e
		}
	}
}

// searchWithRg runs rg --json and parses its output into GrepResults.
// rg exit code 1 means "no matches" — not an error.
func searchWithRg(workDir, pattern, include, exclude string, caseSensitive, isRegex bool) ([]GrepResult, error) {
	if pattern == "" {
		return nil, nil
	}
	args := []string{"--json"}
	for dir := range ignoredDirs {
		args = append(args, "--glob", "!"+dir)
	}
	for _, g := range splitGlobs(include) {
		args = append(args, "--glob", g)
	}
	for _, g := range splitGlobs(exclude) {
		args = append(args, "--glob", "!"+g)
	}
	if caseSensitive {
		args = append(args, "--case-sensitive")
	} else {
		args = append(args, "--ignore-case")
	}
	// Keep rg's own file handling aligned with searchBuiltin's size policy
	// (maxFileBytes): a file rg would skip for being too large can't hand
	// us an oversized single-line match record either. Must come before
	// the "--" below — anything after "--" is positional (pattern, then
	// paths), so a flag placed after it would be parsed as a literal path
	// argument instead and make rg fail immediately.
	args = append(args, "--max-filesize", fmt.Sprintf("%d", maxFileBytes))
	if isRegex {
		args = append(args, "--regexp", pattern)
	} else {
		args = append(args, "--fixed-strings", "--", pattern)
	}
	args = append(args, workDir)

	cmd := exec.Command(rgExecutable, args...)
	procutil.SetPgid(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// readCappedLine (rather than bufio.Scanner, which has a fixed max
	// token size and treats exceeding it as a fatal, unrecoverable error)
	// lets an unexpectedly long line be skipped without ever forcing us to
	// kill rg and discard every match found so far. --max-filesize above
	// already keeps rg from handing us a line from an oversized file at
	// all; this is a defensive backstop for the same policy (e.g. a test
	// double, or any future rg build, that doesn't honor the flag) — an
	// over-limit record is simply skipped, not fatal, and read in bounded
	// memory regardless of how long the actual line turns out to be.
	reader := bufio.NewReader(stdout)
	var results []GrepResult
	killedEarly := false
	var readErr error
	for {
		lineBytes, oversized, err := readCappedLine(reader, maxFileBytes)
		if !oversized && len(lineBytes) > 0 {
			line := bytes.TrimRight(lineBytes, "\n\r")
			var msg rgMsg
			if uerr := json.Unmarshal(line, &msg); uerr == nil && msg.Type == "match" {
				var m rgMatchData
				if uerr := json.Unmarshal(msg.Data, &m); uerr == nil {
					matchLine := strings.TrimRight(m.Lines.Text, "\n\r")
					col, matchLen := 0, 0
					if len(m.Submatches) > 0 {
						sm := m.Submatches[0]
						s := min(sm.Start, len(matchLine))
						e := min(sm.End, len(matchLine))
						col = utf8.RuneCountInString(matchLine[:s])
						matchLen = utf8.RuneCountInString(matchLine[:e]) - col
					}
					rel, _ := filepath.Rel(workDir, m.Path.Text)
					results = append(results, GrepResult{
						RelPath:  rel,
						Line:     int(m.LineNumber) - 1, // rg reports 1-based line numbers
						Col:      col,
						MatchLen: matchLen,
						LineText: matchLine,
					})
					if len(results) >= maxGrepResults {
						// Stop reading and kill rg now instead of letting
						// it run to completion: cmd.Wait() below would
						// otherwise block until rg finishes scanning the
						// whole workspace even though we already have all
						// the matches we're going to display.
						killedEarly = true
						_ = procutil.KillGroup(cmd)
						break
					}
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
	}

	if killedEarly {
		_ = cmd.Wait() // reap the killed process; its exit status is meaningless here
		return results, nil
	}

	if readErr != nil {
		// A genuine read error (as opposed to a too-long line, which is
		// handled above without ever reaching this point) — rg may still
		// be running and blocked writing further output into a pipe nobody
		// is draining anymore, so kill it before Wait() instead of risking
		// an indefinite hang.
		_ = procutil.KillGroup(cmd)
		_ = cmd.Wait()
		return nil, readErr
	}

	waitErr := cmd.Wait()
	if waitErr != nil {
		if exit, ok := waitErr.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil, nil // exit 1 = no matches found
		}
		return nil, waitErr
	}
	return results, nil
}

// ---- built-in Go walker (fallback) ----

// fileListCacheTTL bounds how stale candidateFileListCache's enumerated
// file list is allowed to get. A var (not const) so tests can shrink it
// instead of sleeping for a production-sized window.
var fileListCacheTTL = 20 * time.Second

// candidateWalkCount counts how many times walkCandidateFiles has actually
// re-walked the filesystem (as opposed to serving a cached list) — a test
// seam for verifying the cache is actually being hit, not just that results
// are correct.
var candidateWalkCount int64

// candidateFileListCache holds the most recently enumerated candidate file
// list for one workDir — the set of files that pass the ignoredDirs and
// maxFileBytes checks, before this search's own include/exclude globs are
// applied (those vary per search, so they're filtered back in by the
// caller instead of being part of the cache key). Repeated searches over
// the same workspace (adjusting the pattern or include/exclude filters
// between them, or just re-running the same search) skip the walk phase
// entirely as long as the cache hasn't expired. This only helps
// searchBuiltin: the rg backend does its own internal directory walk
// (which also honors real .gitignore files, unlike this cache's
// ignoredDirs-only filtering) so there's no walk on the Go side to cache
// for that path.
var candidateFileListCache struct {
	mu      sync.Mutex
	workDir string
	builtAt time.Time
	paths   []string
	rels    []string
}

// cachedCandidateFiles returns the enumerated candidate file list for
// workDir, reusing the cached list if it's for the same workDir and still
// within fileListCacheTTL, or re-walking and refreshing the cache
// otherwise. Deliberately bounded-staleness rather than exactly invalidated
// on every filesystem change: a file created or deleted during the TTL
// window won't show up/disappear until the next re-walk, but this avoids
// having to wire up filesystem-change notifications (indigo's client
// process has no such signal for arbitrary out-of-band changes — e.g. a
// git checkout — happening outside its own edit/save paths) just to serve
// a cache that's only ever used to speed up interactive, human-paced
// repeated searches in the same session.
func cachedCandidateFiles(workDir string) (paths, rels []string, err error) {
	candidateFileListCache.mu.Lock()
	if candidateFileListCache.workDir == workDir && time.Since(candidateFileListCache.builtAt) < fileListCacheTTL {
		paths, rels = candidateFileListCache.paths, candidateFileListCache.rels
		candidateFileListCache.mu.Unlock()
		return paths, rels, nil
	}
	candidateFileListCache.mu.Unlock()

	paths, rels, err = walkCandidateFiles(workDir)
	if err != nil {
		return nil, nil, err
	}

	candidateFileListCache.mu.Lock()
	candidateFileListCache.workDir = workDir
	candidateFileListCache.builtAt = time.Now()
	candidateFileListCache.paths = paths
	candidateFileListCache.rels = rels
	candidateFileListCache.mu.Unlock()

	return paths, rels, nil
}

// walkCandidateFiles walks workDir collecting every file that passes the
// ignoredDirs and maxFileBytes checks, in walk order. It applies no
// include/exclude glob filtering — those are per-search and applied
// separately by the caller against this (potentially cached) list.
func walkCandidateFiles(workDir string) (paths, rels []string, err error) {
	atomic.AddInt64(&candidateWalkCount, 1)
	walkErr := filepath.WalkDir(workDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			if ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > int64(maxFileBytes) {
			return nil
		}
		rel, _ := filepath.Rel(workDir, path)
		paths = append(paths, path)
		rels = append(rels, rel)
		return nil
	})
	return paths, rels, walkErr
}

// searchBuiltin searches all text files under workDir for pattern, using
// caseSensitive/isRegex exactly as given. Only the first match per line is
// reported.
func searchBuiltin(workDir, pattern, include, exclude string, caseSensitive, isRegex bool) ([]GrepResult, error) {
	if pattern == "" {
		return nil, nil
	}
	includes := splitGlobs(include)
	excludes := splitGlobs(exclude)

	// Build a per-line matcher function.
	var matchLine func(line string) (col, length int, ok bool)
	if isRegex {
		expr := pattern
		if !caseSensitive {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, err
		}
		matchLine = func(line string) (int, int, bool) {
			loc := re.FindStringIndex(line)
			if loc == nil {
				return 0, 0, false
			}
			start := utf8.RuneCountInString(line[:loc[0]])
			end := utf8.RuneCountInString(line[:loc[1]])
			return start, end - start, true
		}
	} else {
		sensitive := caseSensitive
		patRunes := []rune(pattern)
		if !sensitive {
			for i, r := range patRunes {
				patRunes[i] = unicode.ToLower(r)
			}
		}
		matchLine = func(line string) (int, int, bool) {
			lineRunes := []rune(line)
			search := lineRunes
			if !sensitive {
				search = make([]rune, len(lineRunes))
				for i, r := range lineRunes {
					search[i] = unicode.ToLower(r)
				}
			}
			n, pl := len(search), len(patRunes)
			for i := 0; i <= n-pl; i++ {
				if grepRunesMatch(search[i:], patRunes) {
					return i, pl, true
				}
			}
			return 0, 0, false
		}
	}

	// Enumerate eligible files first — this phase is cheap (stat, no file
	// content read) and stays sequential, since directory walking itself
	// isn't the bottleneck on large trees; reading and matching file
	// *contents* is, and that's what gets parallelized below. The
	// ignoredDirs/size-filtered candidate list (everything *except* this
	// search's own include/exclude globs) is cached across calls for the
	// same workDir — see candidateFileListCache — since re-walking a large
	// tree is the same cost whether or not the user changed their pattern
	// or glob filters between searches.
	allPaths, allRels, walkErr := cachedCandidateFiles(workDir)
	if walkErr != nil {
		return nil, walkErr
	}
	var paths, rels []string
	for i, rel := range allRels {
		if matchGlobs(includes, excludes, rel) {
			paths = append(paths, allPaths[i])
			rels = append(rels, rel)
		}
	}

	// Scan files across a worker pool. Each worker writes only to its own
	// index of perFile, so the final concatenation below reproduces the
	// exact file order the old fully-sequential walk produced regardless of
	// which worker finishes which file first — no ordering surprises from
	// parallelizing this.
	perFile := make([][]GrepResult, len(paths))
	var matched int64 // approximate running total, used only to skip starting new files once we're well past the cap

	numWorkers := runtime.GOMAXPROCS(0)
	if numWorkers > len(paths) {
		numWorkers = len(paths)
	}
	if numWorkers > 16 {
		numWorkers = 16
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	idxCh := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idxCh {
				if atomic.LoadInt64(&matched) >= maxGrepResults {
					continue
				}
				var fileResults []GrepResult
				grepFile(rels[i], paths[i], matchLine, &fileResults)
				if len(fileResults) > 0 {
					perFile[i] = fileResults
					atomic.AddInt64(&matched, int64(len(fileResults)))
				}
			}
		}()
	}
	for i := range paths {
		idxCh <- i
	}
	close(idxCh)
	wg.Wait()

	var results []GrepResult
	for _, fr := range perFile {
		results = append(results, fr...)
	}
	if len(results) > maxGrepResults {
		results = results[:maxGrepResults]
	}
	return results, nil
}

// matchGlob reports whether relPath matches the user-supplied file glob.
// It handles:
//   - "*.go"         → basename match (no / in glob)
//   - "src/*.go"     → full relPath match
//   - "**/foo.go"    → basename match at any depth, including top level
//   - "src/**/*.go"  → *.go anywhere under src/, at any depth (including
//     directly inside src/), ripgrep/gitignore-style globstar
//   - "src/"         → directory prefix match
func matchGlob(glob, relPath string) bool {
	relPath = filepath.ToSlash(relPath)
	glob = filepath.ToSlash(glob)

	// Directory prefix: "src/" matches anything under src/.
	if strings.HasSuffix(glob, "/") {
		return strings.HasPrefix(relPath, glob) || strings.HasPrefix(relPath, strings.TrimSuffix(glob, "/"))
	}
	// No path separator in glob: match against basename.
	if !strings.Contains(glob, "/") {
		ok, _ := filepath.Match(glob, filepath.Base(relPath))
		return ok
	}
	return matchGlobSegments(strings.Split(glob, "/"), strings.Split(relPath, "/"))
}

// matchGlobSegments matches "/"-split glob segments against "/"-split path
// segments. A "**" segment matches zero or more path segments (ripgrep's
// globstar); every other segment matches exactly one path segment via
// filepath.Match, which already handles *, ?, and [...] correctly since
// none of those cross a "/" boundary within a single segment.
func matchGlobSegments(globSegs, pathSegs []string) bool {
	if len(globSegs) == 0 {
		return len(pathSegs) == 0
	}
	if globSegs[0] == "**" {
		if matchGlobSegments(globSegs[1:], pathSegs) {
			return true
		}
		if len(pathSegs) == 0 {
			return false
		}
		return matchGlobSegments(globSegs, pathSegs[1:])
	}
	if len(pathSegs) == 0 {
		return false
	}
	if ok, _ := filepath.Match(globSegs[0], pathSegs[0]); !ok {
		return false
	}
	return matchGlobSegments(globSegs[1:], pathSegs[1:])
}

func grepFile(rel, absPath string, matchLine func(string) (int, int, bool), results *[]GrepResult) {
	f, err := os.Open(absPath)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		raw := scanner.Bytes()
		// Skip binary files: check first line for null bytes.
		if lineNum == 0 {
			for _, b := range raw {
				if b == 0 {
					return
				}
			}
		}
		text := string(raw)
		col, length, ok := matchLine(text)
		if ok {
			*results = append(*results, GrepResult{
				RelPath:  rel,
				Line:     lineNum,
				Col:      col,
				MatchLen: length,
				LineText: text,
			})
			if len(*results) >= maxGrepResults {
				return
			}
		}
		lineNum++
	}
}

// grepRegexExpr mirrors the client-side regexExpr: pattern starts with \,
// trailing \ is optional.
func grepRegexExpr(pattern string) (expr string, ok bool) {
	if len(pattern) == 0 || pattern[0] != '\\' {
		return "", false
	}
	expr = pattern[1:]
	if len(expr) > 0 && expr[len(expr)-1] == '\\' {
		expr = expr[:len(expr)-1]
	}
	return expr, true
}

func grepSmartCase(pattern string) bool {
	for _, r := range pattern {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func grepRunesMatch(line, pat []rune) bool {
	for i, r := range pat {
		if i >= len(line) || line[i] != r {
			return false
		}
	}
	return true
}

// ---- grepPicker UI ----

type grepPicker struct {
	workDir   string
	pattern   string
	include   string // optional include glob filter (e.g. "*.go", "src/")
	exclude   string // optional exclude glob filter (e.g. "vendor/")
	results   []GrepResult
	cursor    int
	width     int
	height    int
	searching bool
	errMsg    string
	seq       int // request sequence this picker is waiting on; see grepResultsMsg
}

// grepResultsMsg delivers async search results back to the App. seq is the
// App's grepSeq counter value at the time the request was issued — a newer
// grep bumps the counter, so a slower older request's results (which could
// otherwise arrive after and overwrite a newer one's) are identifiable and
// discarded instead of applied.
type grepResultsMsg struct {
	seq     int
	results []GrepResult
	err     error
}

// grepPickedMsg is sent when the user confirms a result.
type grepPickedMsg struct {
	absPath  string
	line     int // 0-based
	col      int // 0-based column of match start
	matchLen int // rune length of match
}

// grepCancelledMsg is sent when the user presses Esc.
type grepCancelledMsg struct{}

func (gp *grepPicker) moveUp() {
	if gp.cursor > 0 {
		gp.cursor--
	}
}

func (gp *grepPicker) moveDown() {
	if gp.cursor < len(gp.results)-1 {
		gp.cursor++
	}
}

// View renders the grep picker as a full-screen overlay, matching the style
// of the file picker.
func (gp *grepPicker) View() string {
	const chrome = 5
	innerW := gp.width - 4
	maxItems := gp.height - chrome - 2
	if maxItems < 1 {
		maxItems = 1
	}

	start := 0
	if gp.cursor >= maxItems {
		start = gp.cursor - maxItems + 1
	}
	end := start + maxItems
	if end > len(gp.results) {
		end = len(gp.results)
	}

	pad := strings.Repeat(" ", innerW)
	// ANSI-aware (via lipgloss.Width/ansiTruncate, not a raw rune count):
	// grepResultLine can embed a bold-preserving escape sequence around the
	// matched text (see boldPreserving in searchreplace.go), which a plain
	// rune-count clamp would misjudge as extra visible width and truncate
	// or pad incorrectly.
	clamp := func(s string) string {
		w := lipgloss.Width(s)
		if w > innerW {
			return ansiTruncate(s, innerW)
		}
		return s + strings.Repeat(" ", innerW-w)
	}

	var sb strings.Builder

	// Title
	subject := gp.pattern
	if gp.include != "" || gp.exclude != "" {
		var parts []string
		if gp.include != "" {
			parts = append(parts, "in:"+gp.include)
		}
		if gp.exclude != "" {
			parts = append(parts, "!"+gp.exclude)
		}
		subject = fmt.Sprintf("%s  %s", gp.pattern, strings.Join(parts, " "))
	}
	var title string
	switch {
	case gp.errMsg != "":
		title = fmt.Sprintf("  Search: %s  [error]", subject)
	case gp.searching:
		title = fmt.Sprintf("  Search: %s  [searching…]", subject)
	case len(gp.results) >= maxGrepResults:
		title = fmt.Sprintf("  Search: %s  [%d+ results]", subject, maxGrepResults)
	default:
		title = fmt.Sprintf("  Search: %s  [%d results]", subject, len(gp.results))
	}
	sb.WriteString(pickerTitleStyle.Render(clamp(title)))
	sb.WriteByte('\n')

	hint := clamp("  ↑↓/jk navigate  Enter open  Esc cancel")
	sb.WriteString(pickerQueryStyle.Render(hint))
	sb.WriteByte('\n')

	sb.WriteString(pickerItemStyle.Render(pad))
	sb.WriteByte('\n')

	switch {
	case gp.errMsg != "":
		sb.WriteString(pickerItemStyle.Render(clamp("  Error: " + gp.errMsg)))
		sb.WriteByte('\n')
		for i := 1; i < maxItems; i++ {
			sb.WriteString(pickerItemStyle.Render(pad))
			sb.WriteByte('\n')
		}
	case gp.searching:
		sb.WriteString(pickerItemStyle.Render(clamp("  Searching…")))
		sb.WriteByte('\n')
		for i := 1; i < maxItems; i++ {
			sb.WriteString(pickerItemStyle.Render(pad))
			sb.WriteByte('\n')
		}
	default:
		for i := start; i < end; i++ {
			r := gp.results[i]
			line := grepResultLine(r, innerW-2)
			if i == gp.cursor {
				sb.WriteString(pickerSelStyle.Render(clamp("  " + line)))
			} else {
				sb.WriteString(pickerItemStyle.Render(clamp("  " + line)))
			}
			sb.WriteByte('\n')
		}
		for i := end - start; i < maxItems; i++ {
			sb.WriteString(pickerItemStyle.Render(pad))
			sb.WriteByte('\n')
		}
	}

	// Bottom status: count of visible vs total.
	var status string
	if !gp.searching && gp.errMsg == "" && len(gp.results) > 0 {
		status = fmt.Sprintf("  %d / %d", gp.cursor+1, len(gp.results))
	}
	sb.WriteString(pickerItemStyle.Render(clamp(status)))

	body := sb.String()

	boxW := innerW + 4
	boxH := maxItems + chrome
	box := pickerBorderStyle.Width(innerW).Height(boxH - 2).Render(body)

	col := max(0, (gp.width-boxW)/2)
	row := max(0, (gp.height-boxH)/2)

	var out strings.Builder
	blank := strings.Repeat(" ", gp.width)
	for i := 0; i < row; i++ {
		out.WriteString(blank)
		out.WriteByte('\n')
	}
	for _, bline := range strings.Split(box, "\n") {
		out.WriteString(strings.Repeat(" ", col))
		out.WriteString(bline)
		out.WriteByte('\n')
	}
	return out.String()
}

// grepResultLine formats one result as "path:line: content", windowed to at
// most maxW runes so the match stays visible: whatever width remains once
// the match itself is reserved is split (via splitContext, shared with the
// search/replace dialog's results list) between context before and after
// the match, as a best-effort centering that degrades gracefully near
// either end of the line.
func grepResultLine(r GrepResult, maxW int) string {
	label := fmt.Sprintf("%s:%d: ", r.RelPath, r.Line+1)
	content := strings.TrimLeft(r.LineText, " \t")
	trimmedCount := len([]rune(r.LineText)) - len([]rune(content))
	matchStart := max(r.Col-trimmedCount, 0)
	matchEnd := max(matchStart+r.MatchLen, matchStart)

	avail := maxW - len([]rune(label))
	if avail < 1 {
		full := []rune(label + content)
		if len(full) <= maxW {
			return string(full)
		}
		if maxW < 1 {
			return ""
		}
		return string(full[:max(maxW-1, 0)]) + "…"
	}

	cr := []rune(content)
	if matchEnd > len(cr) {
		matchEnd = len(cr)
	}
	if matchStart > matchEnd {
		matchStart = matchEnd
	}
	before := string(cr[:matchStart])
	match := string(cr[matchStart:matchEnd])
	after := string(cr[matchEnd:])

	beforeShown, afterShown := splitContext(before, after, avail-len([]rune(match)))
	line := label + beforeShown + match + afterShown
	// The match itself may be wider than avail (rare); hard-clip as a last
	// resort rather than overflowing the caller's width budget. This check
	// (and everything above it) works in plain runes, so it must run before
	// the match is styled below — bold's escape codes aren't real display
	// width but would still throw off a rune count.
	if lr := []rune(line); len(lr) > maxW {
		if maxW < 1 {
			return ""
		}
		return string(lr[:maxW-1]) + "…"
	}

	// boldPreserving (not a plain lipgloss Render) because this row is
	// about to be wrapped in pickerItemStyle/pickerSelStyle, both of which
	// set a background — a full-reset style Render nested inside would cut
	// a visible gap in it.
	return label + beforeShown + boldPreserving(match) + afterShown
}
