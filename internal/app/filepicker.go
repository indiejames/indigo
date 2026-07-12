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

// ignoredDirs are never shown in the file browser.
var ignoredDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true,
	".svn": true, ".hg": true, "__pycache__": true, ".cache": true,
}

// pickerEntry is one row in the directory browser.
type pickerEntry struct {
	name  string
	isDir bool
}

// filePicker is the file-selection overlay.
//
// Browse mode (query == ""): shows the contents of currentDir — directories
// first, then files, with ".." prepended when not at the project root.
//
// Search mode (query != ""): shows a globally fuzzy-filtered flat file list,
// same as the previous behaviour. Clearing the query returns to browse mode.
type filePicker struct {
	workDir     string
	currentDir  string        // workspace-relative path; "" = project root
	entries     []pickerEntry // browse-mode rows
	all         []string      // all workspace-relative file paths (for search)
	filtered    []string      // search-mode results
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
	fp := &filePicker{
		workDir:     workDir,
		width:       w,
		height:      h,
		fuzzySearch: fuzzySearch,
	}
	fp.all = collectFiles(workDir)
	fp.entries = fp.buildEntries()
	return fp
}

// browseMode reports whether the picker is showing the directory browser
// (query is empty) rather than the global search list.
func (fp *filePicker) browseMode() bool { return fp.query == "" }

// buildEntries reads currentDir and returns a sorted entry list:
// ".." (when not at root), then directories, then files.
func (fp *filePicker) buildEntries() []pickerEntry {
	absDir := fp.workDir
	if fp.currentDir != "" {
		absDir = filepath.Join(fp.workDir, fp.currentDir)
	}

	des, err := os.ReadDir(absDir)
	if err != nil {
		return nil
	}

	var dirs, files []pickerEntry
	for _, de := range des {
		if ignoredDirs[de.Name()] {
			continue
		}
		if de.IsDir() {
			dirs = append(dirs, pickerEntry{name: de.Name(), isDir: true})
		} else {
			files = append(files, pickerEntry{name: de.Name(), isDir: false})
		}
	}
	// os.ReadDir returns entries sorted, but be explicit.
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].name < dirs[j].name })
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	var result []pickerEntry
	if fp.currentDir != "" {
		result = append(result, pickerEntry{name: "..", isDir: true})
	}
	result = append(result, dirs...)
	result = append(result, files...)
	return result
}

// navigateInto descends into the named subdirectory.
func (fp *filePicker) navigateInto(name string) {
	if fp.currentDir == "" {
		fp.currentDir = name
	} else {
		fp.currentDir = filepath.Join(fp.currentDir, name)
	}
	fp.cursor = 0
	fp.entries = fp.buildEntries()
}

// navigateUp ascends one directory level. No-op at the project root.
func (fp *filePicker) navigateUp() {
	if fp.currentDir == "" {
		return
	}
	parent := filepath.Dir(fp.currentDir)
	if parent == "." {
		parent = ""
	}
	fp.currentDir = parent
	fp.cursor = 0
	fp.entries = fp.buildEntries()
}

// collectFiles walks root and returns all workspace-relative file paths.
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
		// Returning to browse mode — reset filtered and rebuild directory entries.
		fp.filtered = fp.all
		fp.entries = fp.buildEntries()
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

// pickerScore ranks a fuzzy match result.
func pickerScore(query, path string, fuzzyLibScore int) int {
	base := filepath.Base(path)
	lq := strings.ToLower(query)
	lb := strings.ToLower(base)
	lp := strings.ToLower(path)

	score := fuzzyLibScore
	switch {
	case lb == lq:
		score += 10000
	case strings.HasPrefix(lb, lq):
		score += 5000
	case strings.HasSuffix(lb, lq):
		score += 3000
	case strings.Contains(lb, lq):
		score += 2000
	case strings.HasSuffix(lp, lq):
		score += 1000
	}
	score -= strings.Count(path, string(filepath.Separator)) * 10
	return score
}

func (fp *filePicker) moveUp() {
	if fp.cursor > 0 {
		fp.cursor--
	}
}

func (fp *filePicker) moveDown() {
	limit := len(fp.entries) - 1
	if !fp.browseMode() {
		limit = len(fp.filtered) - 1
	}
	if fp.cursor < limit {
		fp.cursor++
	}
}

// selectedEntry returns the highlighted browse-mode entry, or nil if none.
func (fp *filePicker) selectedEntry() *pickerEntry {
	if !fp.browseMode() || fp.cursor < 0 || fp.cursor >= len(fp.entries) {
		return nil
	}
	return &fp.entries[fp.cursor]
}

// selectedPath returns the absolute path for the highlighted item.
// Returns "" in browse mode when a directory is selected.
func (fp *filePicker) selectedPath() string {
	if fp.browseMode() {
		e := fp.selectedEntry()
		if e == nil || e.isDir {
			return ""
		}
		rel := e.name
		if fp.currentDir != "" {
			rel = filepath.Join(fp.currentDir, e.name)
		}
		return filepath.Join(fp.workDir, rel)
	}
	if len(fp.filtered) == 0 {
		return ""
	}
	return filepath.Join(fp.workDir, fp.filtered[fp.cursor])
}

// breadcrumb returns the path label shown in the title row.
func (fp *filePicker) breadcrumb() string {
	base := filepath.Base(fp.workDir)
	if fp.currentDir == "" {
		return base + "/"
	}
	parts := strings.Split(fp.currentDir, string(filepath.Separator))
	all := append([]string{base}, parts...)
	return strings.Join(all, " / ") + "/"
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

	pickerDirStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#1E2A38")).
			Foreground(lipgloss.Color("#88BBDD"))

	pickerDirSelStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#2D5F8A")).
				Foreground(lipgloss.Color("#AADDFF")).
				Bold(true)

	pickerTitleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#1E2A38")).
				Foreground(lipgloss.Color("#4488CC")).
				Bold(true)
)

// View renders the picker as a full-screen overlay.
func (fp *filePicker) View() string {
	// chrome: title(1) + query(1) + divider(1) + hint(1) + border(2) = 6
	const chrome = 6
	innerW := fp.width - 4 // border(2) + padding(2)
	if innerW < 10 {
		innerW = 10
	}
	maxItems := fp.height - chrome - 2
	if maxItems < 1 {
		maxItems = 1
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

	// Title / breadcrumb row.
	if fp.browseMode() {
		sb.WriteString(pickerTitleStyle.Render(clamp("  " + fp.breadcrumb())))
	} else {
		sb.WriteString(pickerTitleStyle.Render(clamp("  Open File")))
	}
	sb.WriteByte('\n')

	// Query row.
	sb.WriteString(pickerQueryStyle.Render(clamp("> " + fp.query)))
	sb.WriteByte('\n')

	// Divider.
	sb.WriteString(pickerItemStyle.Render(pad))
	sb.WriteByte('\n')

	// Collect display strings and dir flags for the current mode.
	var labels []string
	var isDir []bool
	if fp.browseMode() {
		for _, e := range fp.entries {
			if e.isDir {
				labels = append(labels, e.name+"/")
			} else {
				labels = append(labels, e.name)
			}
			isDir = append(isDir, e.isDir)
		}
	} else {
		for _, p := range fp.filtered {
			labels = append(labels, p)
			isDir = append(isDir, false)
		}
	}

	// Scroll window so the cursor stays visible.
	start := 0
	if fp.cursor >= maxItems {
		start = fp.cursor - maxItems + 1
	}
	end := start + maxItems
	if end > len(labels) {
		end = len(labels)
	}

	for i := start; i < end; i++ {
		line := clamp("  " + labels[i])
		dir := i < len(isDir) && isDir[i]
		switch {
		case i == fp.cursor && dir:
			sb.WriteString(pickerDirSelStyle.Render(line))
		case i == fp.cursor:
			sb.WriteString(pickerSelStyle.Render(line))
		case dir:
			sb.WriteString(pickerDirStyle.Render(line))
		default:
			sb.WriteString(pickerItemStyle.Render(line))
		}
		sb.WriteByte('\n')
	}

	// Pad empty rows.
	for i := end - start; i < maxItems; i++ {
		sb.WriteString(pickerItemStyle.Render(pad))
		sb.WriteByte('\n')
	}

	// Hint row.
	var hint string
	if fp.browseMode() {
		hint = "  ↑/↓ navigate   Enter open/cd   Bksp up   Esc cancel"
	} else {
		hint = "  ↑/↓ navigate   Enter open   Bksp clear/up   Esc cancel"
	}
	sb.WriteString(pickerItemStyle.Render(clamp(hint)))

	body := sb.String()

	boxW := innerW + 4
	boxH := maxItems + chrome
	box := pickerBorderStyle.Width(innerW).Height(boxH - 2).Render(body)

	col := max(0, (fp.width-boxW)/2)
	row := max(0, (fp.height-boxH)/2)

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
