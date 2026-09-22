package app

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/indiejames/indigo/internal/client"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"
)

// pickerEntry is one row in the directory browser.
type pickerEntry struct {
	name  string
	isDir bool
}

// filePicker is the file-selection overlay.
//
// Browse mode (query == ""): shows the contents of currentDir — directories
// first, then files, with ".." prepended when not at the project root. If
// recentMode is set, it shows recentFiles (a flat MRU list) instead.
//
// Search mode (query != ""): shows a globally fuzzy-filtered flat file list,
// same as the previous behaviour. Clearing the query returns to browse mode
// (recentMode, if set, is preserved across the round trip).
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

	recentMode  bool     // showing recentFiles instead of the directory browser
	recentFiles []string // workspace-relative paths, most-recently-opened first

	// all is populated asynchronously (see App.startPickerFileScan) rather
	// than by newFilePicker itself: a full recursive workspace walk can take
	// a very long time on a large/slow directory tree (a real report: a
	// home directory with no project-scoped root), and doing it synchronously
	// inside Update blocked the whole UI — including the keypress meant to
	// open a different picker or cancel. Browse mode is a single readdir and
	// stays comparatively cheap, but it is a round trip now too and so has its
	// own loadingDir/dirSeq pair. loadingAll is
	// true until the scan for seq completes; seq guards against a stale scan
	// (from a picker that's since closed, or been reopened for a different
	// directory) overwriting a newer one's results — see pickerFilesMsg.
	seq        int
	loadingAll bool

	// loadingDir is browse mode's equivalent of loadingAll: the directory
	// listing is now a round trip to the server, because the server is the
	// process that can see the directory. dirSeq identifies which request the
	// picker is waiting on, so a slower listing for a directory the user has
	// already navigated out of cannot overwrite the one they are looking at —
	// the same hazard pickerFilesMsg's seq guards, now reachable by holding
	// down Backspace.
	loadingDir bool
	dirSeq     int

	// recentSeq identifies the recent-files filter request this picker is
	// waiting on, for the same reason dirSeq exists: the picker can be closed
	// and reopened while one is in flight.
	recentSeq int
}

// showingRecent reports whether the recent-files list is currently displayed.
func (fp *filePicker) showingRecent() bool { return fp.browseMode() && fp.recentMode }

// pickedMsg is sent when the user selects a file.
type pickedMsg struct{ absPath string }

// pickerCancelledMsg is sent when the user presses Esc.
type pickerCancelledMsg struct{}

// pickerFilesMsg carries the result of a background workspace file scan
// (see App.startPickerFileScan). seq is checked against the current
// picker's own seq before applying — a scan whose picker has since closed,
// or been reopened for a different directory, is discarded rather than
// clobbering a newer scan's results.
type pickerFilesMsg struct {
	seq   int
	files []string
}

// startPickerFileScan asks the server to enumerate the workspace for the
// picker's fuzzy-search list.
//
// The walk runs where the files are. That used to be here, which was correct
// only for as long as the client and the workspace shared a machine — see
// internal/workspacefs.
//
// seq still guards the result, for the same reason it always did: a slower
// older scan must not overwrite a newer one's list. A round trip only makes
// that more likely, not less.
func (a *App) startPickerFileScan(workDir string) tea.Cmd {
	a.pickerFilesSeq++
	seq := a.pickerFilesSeq
	a.picker.seq = seq
	rpc := a.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), workspaceScanTimeout)
		defer cancel()
		files, err := rpc.ListWorkspaceFiles(ctx)
		if err != nil {
			// A failed scan leaves the fuzzy list empty rather than stale;
			// browse mode still works, which is the useful half.
			appLog("picker file scan failed: %v", err)
			return pickerFilesMsg{seq: seq}
		}
		return pickerFilesMsg{seq: seq, files: files}
	}
}

// pushIgnoredDirs sends config.toml's picker_ignore_dirs to the server.
//
// Fire-and-forget: the set only affects what a later listing hides, so a failed
// push costs one stale listing and is not worth interrupting anything for. The
// next config tick or picker open sends it again.
func (a App) pushIgnoredDirs() tea.Cmd {
	if a.rpc == nil || a.cfg == nil {
		return nil
	}
	rpc := a.rpc
	dirs := append([]string(nil), a.cfg.PickerIgnoreDirs...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rpc.SetIgnoredDirs(ctx, dirs); err != nil {
			appLog("pushing picker_ignore_dirs failed: %v", err)
		}
		return nil
	}
}

// loadPickerDir asks the server for the listing of the picker's current
// directory. Pair it with any state change that moves the picker — see
// filePicker.beginDirLoad.
func (a *App) loadPickerDir() tea.Cmd {
	if a.picker == nil {
		return nil
	}
	a.pickerDirSeq++
	seq := a.pickerDirSeq
	a.picker.dirSeq = seq
	rpc := a.rpc
	// Sent as a workspace-relative path, and resolved against the workspace
	// root by the server: "" means the root, and the client does not have to
	// know what that root is called on the far side of a container boundary.
	dir := a.picker.currentDir
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listDirTimeout)
		defer cancel()
		entries, err := rpc.ListDir(ctx, dir)
		if err != nil {
			appLog("picker list dir %q failed: %v", dir, err)
			return pickerDirMsg{seq: seq}
		}
		return pickerDirMsg{seq: seq, entries: entries}
	}
}

// filterRecentFiles asks the server which of the recorded recent files are
// still worth showing — still present, not ignored, not gitignored. Every part
// of that question is about the workspace, so it is asked where the workspace
// is.
func (a *App) filterRecentFiles() tea.Cmd {
	if a.picker == nil || len(a.picker.recentFiles) == 0 {
		return nil
	}
	a.pickerRecentSeq++
	seq := a.pickerRecentSeq
	a.picker.recentSeq = seq
	rpc := a.rpc
	rels := append([]string(nil), a.picker.recentFiles...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listDirTimeout)
		defer cancel()
		kept, err := rpc.FilterWorkspaceFiles(ctx, rels)
		if err != nil {
			// Leave the unfiltered list alone rather than blanking it: a stale
			// entry the user can see is better than a list that vanished.
			appLog("recent-files filter failed: %v", err)
			return recentFilesMsg{seq: seq, files: rels}
		}
		return recentFilesMsg{seq: seq, files: kept}
	}
}

// recentFilesMsg carries the filtered recent-files list back to the picker.
type recentFilesMsg struct {
	seq   int
	files []string
}

// pickerDirMsg carries one directory listing back to the picker. seq is checked
// against the picker's current dirSeq on arrival: hold Backspace and several
// listings are in flight at once, and the one that arrives last is not
// necessarily the one for the directory now on screen.
type pickerDirMsg struct {
	seq     int
	entries []client.DirEntry
}

const (
	// listDirTimeout bounds one readdir. Generous for a local disk and still
	// short enough that a wedged mount does not leave the picker blank with no
	// explanation.
	listDirTimeout = 10 * time.Second
	// workspaceScanTimeout bounds a whole-tree walk, which on a large
	// repository over a slow container mount is a different order of cost.
	workspaceScanTimeout = 60 * time.Second
	// grepTimeout bounds a workspace search. Same order as the scan: ripgrep
	// over a large tree is the slowest thing the editor asks the server for.
	grepTimeout = 60 * time.Second
)

// startDir is the workspace-relative directory to browse into initially;
// "" opens at the project root. The global (fuzzy-search) file list isn't
// populated here — see the filePicker.all doc comment — callers must also
// call App.startPickerFileScan to kick that off.
func newFilePicker(workDir, startDir string, w, h int, fuzzySearch bool) *filePicker {
	fp := &filePicker{
		workDir:     workDir,
		currentDir:  startDir,
		width:       w,
		height:      h,
		fuzzySearch: fuzzySearch,
		loadingAll:  true,
	}
	// The first listing arrives asynchronously; callers pair this with
	// App.loadPickerDir, exactly as they already pair it with
	// App.startPickerFileScan for the fuzzy list.
	return fp
}

// beginDirLoad marks the picker as waiting for a listing of currentDir. The
// caller pairs this with App.loadPickerDir, which issues the request; splitting
// them keeps the RPC out of a method that has no way to reach the server.
func (fp *filePicker) beginDirLoad() {
	fp.entries = nil
	fp.loadingDir = true
}

// browseMode reports whether the picker is showing the directory browser
// (query is empty) rather than the global search list.
func (fp *filePicker) browseMode() bool { return fp.query == "" }

// setDirEntries installs a directory listing the server produced, prepending
// ".." when there is a parent to go back to.
//
// The ignore filter that used to happen here now happens on the server: the set
// is config-driven and the config describing that filesystem is the server's.
// What is left is presentation — the parent entry and the ordering the server
// already applied.
func (fp *filePicker) setDirEntries(entries []client.DirEntry) {
	result := make([]pickerEntry, 0, len(entries)+1)
	if fp.currentDir != "" {
		result = append(result, pickerEntry{name: "..", isDir: true})
	}
	for _, e := range entries {
		result = append(result, pickerEntry{name: e.Name, isDir: e.IsDir})
	}
	fp.entries = result
	fp.loadingDir = false
}

// navigateInto descends into the named subdirectory.
func (fp *filePicker) navigateInto(name string) {
	if fp.currentDir == "" {
		fp.currentDir = name
	} else {
		fp.currentDir = filepath.Join(fp.currentDir, name)
	}
	fp.cursor = 0
	fp.beginDirLoad()
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
	fp.beginDirLoad()
}

func (fp *filePicker) setQuery(q string) {
	fp.query = q
	fp.cursor = 0
	if q == "" {
		// Returning to browse mode — reset filtered and rebuild directory entries.
		fp.filtered = fp.all
		fp.beginDirLoad()
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
	switch {
	case fp.showingRecent():
		limit = len(fp.recentFiles) - 1
	case !fp.browseMode():
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
	if fp.showingRecent() {
		if fp.cursor < 0 || fp.cursor >= len(fp.recentFiles) {
			return ""
		}
		return filepath.Join(fp.workDir, fp.recentFiles[fp.cursor])
	}
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
	switch {
	case fp.showingRecent():
		sb.WriteString(pickerTitleStyle.Render(clamp("  Recent Files")))
	case fp.browseMode():
		title := "  " + fp.breadcrumb()
		if fp.loadingDir {
			title += "  [listing…]"
		}
		sb.WriteString(pickerTitleStyle.Render(clamp(title)))
	default:
		title := "  Open File"
		if fp.loadingAll {
			title += "  [scanning files…]"
		}
		sb.WriteString(pickerTitleStyle.Render(clamp(title)))
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
	switch {
	case fp.showingRecent():
		labels = append(labels, fp.recentFiles...)
		isDir = make([]bool, len(fp.recentFiles))
	case fp.browseMode():
		for _, e := range fp.entries {
			if e.isDir {
				labels = append(labels, e.name+"/")
			} else {
				labels = append(labels, e.name)
			}
			isDir = append(isDir, e.isDir)
		}
	default:
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
	switch {
	case fp.showingRecent():
		hint = "  ↑/↓ navigate   Enter open   Tab show all   Esc cancel"
	case fp.browseMode():
		hint = "  ↑/↓ navigate   Enter open/cd   Bksp up   Esc cancel"
		if len(fp.recentFiles) > 0 {
			hint = "  ↑/↓ navigate   Enter open/cd   Bksp up   Tab recent   Esc cancel"
		}
	default:
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
