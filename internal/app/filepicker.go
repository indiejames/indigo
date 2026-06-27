package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

// ignoredDirs are never walked when building the file list.
var ignoredDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true,
	".svn": true, ".hg": true, "__pycache__": true, ".cache": true,
}

// filePicker is the file-selection overlay.
type filePicker struct {
	workDir     string
	all         []string // workspace-relative paths, sorted
	filtered    []string // current filtered view
	query       string
	cursor      int
	width       int
	height      int
	fuzzySearch bool
}

// pickedMsg is sent when the user selects a file.
type pickedMsg struct{ absPath string }

// pickerCancelledMsg is sent when the user presses Esc.
type pickerCancelledMsg struct{}

func newFilePicker(workDir string, w, h int, fuzzySearch bool) *filePicker {
	fp := &filePicker{workDir: workDir, width: w, height: h, fuzzySearch: fuzzySearch}
	fp.all = collectFiles(workDir)
	fp.filtered = fp.all
	return fp
}

func collectFiles(root string) []string {
	var paths []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		paths = append(paths, rel)
		return nil
	})
	sort.Strings(paths)
	return paths
}

func (fp *filePicker) setQuery(q string) {
	fp.query = q
	fp.cursor = 0
	if q == "" {
		fp.filtered = fp.all
		return
	}
	if fp.fuzzySearch {
		matches := fuzzy.Find(q, fp.all)
		type candidate struct {
			path  string
			score int
		}
		cs := make([]candidate, len(matches))
		for i, m := range matches {
			cs[i] = candidate{path: fp.all[m.Index], score: pickerScore(q, fp.all[m.Index], m.Score)}
		}
		sort.Slice(cs, func(i, j int) bool { return cs[i].score > cs[j].score })
		fp.filtered = make([]string, len(cs))
		for i, c := range cs {
			fp.filtered[i] = c.path
		}
	} else {
		lower := strings.ToLower(q)
		fp.filtered = nil
		for _, p := range fp.all {
			if strings.Contains(strings.ToLower(p), lower) {
				fp.filtered = append(fp.filtered, p)
			}
		}
	}
}

// pickerScore returns a ranking score for a fuzzy match.
// Higher is better. Basename matches are heavily weighted so that typing a
// filename brings exact or prefix matches to the top regardless of path depth.
func pickerScore(query, path string, fuzzyLibScore int) int {
	base := filepath.Base(path)
	lq := strings.ToLower(query)
	lb := strings.ToLower(base)
	lp := strings.ToLower(path)

	score := fuzzyLibScore

	switch {
	case lb == lq:
		score += 10000 // exact filename match
	case strings.HasPrefix(lb, lq):
		score += 5000 // filename starts with query
	case strings.HasSuffix(lb, lq):
		score += 3000 // filename ends with query (e.g. extension match)
	case strings.Contains(lb, lq):
		score += 2000 // query is a substring of the filename
	case strings.HasSuffix(lp, lq):
		score += 1000 // full query matches the tail of the path
	}

	// Prefer shallower paths when scores are otherwise equal.
	score -= strings.Count(path, string(filepath.Separator)) * 10

	return score
}

func (fp *filePicker) moveUp() {
	if fp.cursor > 0 {
		fp.cursor--
	}
}

func (fp *filePicker) moveDown() {
	if fp.cursor < len(fp.filtered)-1 {
		fp.cursor++
	}
}

func (fp *filePicker) selected() string {
	if len(fp.filtered) == 0 {
		return ""
	}
	rel := fp.filtered[fp.cursor]
	return filepath.Join(fp.workDir, rel)
}

// ---- styles ----

var (
	pickerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#4488CC")).
				Background(lipgloss.Color("#1E2A38"))

	pickerQueryStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#1E2A38")).
				Foreground(lipgloss.Color("#CCDDEE"))

	pickerItemStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#1E2A38")).
			Foreground(lipgloss.Color("#AABBCC"))

	pickerSelStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#2D5F8A")).
			Foreground(lipgloss.Color("#FFFFFF"))

	pickerTitleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#1E2A38")).
				Foreground(lipgloss.Color("#4488CC")).
				Bold(true)
)

// View renders the picker as a full-screen overlay.
func (fp *filePicker) View() string {
	// Reserve space: border (2) + title (1) + query (1) + divider (1)
	const chrome = 5
	innerW := fp.width - 4 // 2 for border + 2 for padding
	maxItems := fp.height - chrome - 2
	if maxItems < 1 {
		maxItems = 1
	}

	// Scroll window so cursor is visible.
	start := 0
	if fp.cursor >= maxItems {
		start = fp.cursor - maxItems + 1
	}
	end := start + maxItems
	if end > len(fp.filtered) {
		end = len(fp.filtered)
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

	title := clamp("  Open file")
	sb.WriteString(pickerTitleStyle.Render(title))
	sb.WriteByte('\n')

	prompt := "> " + fp.query
	qLine := clamp(prompt)
	sb.WriteString(pickerQueryStyle.Render(qLine))
	sb.WriteByte('\n')

	sb.WriteString(pickerItemStyle.Render(pad))
	sb.WriteByte('\n')

	for i := start; i < end; i++ {
		line := clamp("  " + fp.filtered[i])
		if i == fp.cursor {
			sb.WriteString(pickerSelStyle.Render(line))
		} else {
			sb.WriteString(pickerItemStyle.Render(line))
		}
		sb.WriteByte('\n')
	}

	// Pad remaining rows.
	for i := end - start; i < maxItems; i++ {
		sb.WriteString(pickerItemStyle.Render(pad))
		sb.WriteByte('\n')
	}

	hint := clamp("  ↑/↓ navigate  Enter open  Esc cancel")
	sb.WriteString(pickerItemStyle.Render(hint))

	body := sb.String()

	// Wrap in border, centered on screen.
	boxW := innerW + 4
	boxH := maxItems + chrome
	boxStyle := pickerBorderStyle.Width(innerW).Height(boxH - 2)
	box := boxStyle.Render(body)

	col := max(0, (fp.width-boxW)/2)
	row := max(0, (fp.height-boxH)/2)

	lines := strings.Split(os.Expand("", func(string) string { return "" }), "\n") // blank canvas
	_ = lines

	// Build the full-screen view: blank lines above, then box.
	var out strings.Builder
	blank := strings.Repeat(" ", fp.width)
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
