package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxGrepResults = 500
const maxFileBytes = 5 * 1024 * 1024 // 5 MB

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
// built-in Go walker otherwise.
func searchWorkspace(workDir, pattern string) ([]GrepResult, error) {
	if rgAvailable() {
		return searchWithRg(workDir, pattern)
	}
	return searchBuiltin(workDir, pattern)
}

// rgAvailable reports whether rg is on the PATH.
func rgAvailable() bool {
	_, err := exec.LookPath("rg")
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

// searchWithRg runs rg --json and parses its output into GrepResults.
// rg exit code 1 means "no matches" — not an error.
func searchWithRg(workDir, pattern string) ([]GrepResult, error) {
	if pattern == "" {
		return nil, nil
	}
	args := []string{"--json"}
	for dir := range ignoredDirs {
		args = append(args, "--glob", "!"+dir)
	}
	if expr, isRe := grepRegexExpr(pattern); isRe {
		if expr == "" {
			return nil, nil
		}
		args = append(args, "--regexp", expr)
	} else {
		args = append(args, "--fixed-strings", "--smart-case", "--", pattern)
	}
	args = append(args, workDir)

	cmd := exec.Command("rg", args...)
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil, nil // exit 1 = no matches found
		}
		return nil, err
	}

	var results []GrepResult
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() && len(results) < maxGrepResults {
		var msg rgMsg
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil || msg.Type != "match" {
			continue
		}
		var m rgMatchData
		if err := json.Unmarshal(msg.Data, &m); err != nil {
			continue
		}
		line := strings.TrimRight(m.Lines.Text, "\n\r")
		col, matchLen := 0, 0
		if len(m.Submatches) > 0 {
			sm := m.Submatches[0]
			s := min(sm.Start, len(line))
			e := min(sm.End, len(line))
			col = utf8.RuneCountInString(line[:s])
			matchLen = utf8.RuneCountInString(line[:e]) - col
		}
		rel, _ := filepath.Rel(workDir, m.Path.Text)
		results = append(results, GrepResult{
			RelPath:  rel,
			Line:     int(m.LineNumber) - 1, // rg reports 1-based line numbers
			Col:      col,
			MatchLen: matchLen,
			LineText: line,
		})
	}
	return results, nil
}

// ---- built-in Go walker (fallback) ----

// searchBuiltin searches all text files under workDir for pattern.
// Pattern syntax is the same as within-buffer search: plain text (smart-case)
// or \expr\ for a Go regexp. Only the first match per line is reported.
func searchBuiltin(workDir, pattern string) ([]GrepResult, error) {
	if pattern == "" {
		return nil, nil
	}

	// Build a per-line matcher function.
	var matchLine func(line string) (col, length int, ok bool)
	if expr, isRe := grepRegexExpr(pattern); isRe {
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
		sensitive := grepSmartCase(pattern)
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

	var results []GrepResult
	err := filepath.WalkDir(workDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			if ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(results) >= maxGrepResults {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxFileBytes {
			return nil
		}
		rel, _ := filepath.Rel(workDir, path)
		grepFile(rel, path, matchLine, &results)
		return nil
	})
	return results, err
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
	results   []GrepResult
	cursor    int
	width     int
	height    int
	searching bool
	errMsg    string
}

// grepResultsMsg delivers async search results back to the App.
type grepResultsMsg struct {
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
	clamp := func(s string) string {
		r := []rune(s)
		if len(r) > innerW {
			return string(r[:innerW-1]) + "…"
		}
		return s + strings.Repeat(" ", innerW-len(r))
	}

	var sb strings.Builder

	// Title
	var title string
	switch {
	case gp.errMsg != "":
		title = fmt.Sprintf("  Search: %s  [error]", gp.pattern)
	case gp.searching:
		title = fmt.Sprintf("  Search: %s  [searching…]", gp.pattern)
	case len(gp.results) >= maxGrepResults:
		title = fmt.Sprintf("  Search: %s  [%d+ results]", gp.pattern, maxGrepResults)
	default:
		title = fmt.Sprintf("  Search: %s  [%d results]", gp.pattern, len(gp.results))
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

// grepResultLine formats one result as "path:line: content" truncated to maxW runes.
func grepResultLine(r GrepResult, maxW int) string {
	prefix := fmt.Sprintf("%s:%d:", r.RelPath, r.Line+1)
	content := strings.TrimLeft(r.LineText, " \t")
	full := prefix + " " + content
	runes := []rune(full)
	if len(runes) > maxW {
		return string(runes[:maxW-1]) + "…"
	}
	return full
}
