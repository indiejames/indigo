// indigo-git is a plugin for the indigo editor that provides git integration.
//
// Features:
//   - Current branch name in the status bar
//   - Gutter decorations showing git diff output, in git's own colours: green
//     for added lines, red for deletion points. There is no "modified line"
//     state — like git, a change is a removal plus an addition. The editor
//     draws these two ways: an unobtrusive colour block during ordinary
//     editing, switching to git's own +/- signs (with the line numbers tinted
//     to match) while the inline diff is on.
//   - Fixed-width gutter: always emits a decoration for every visible line so
//     layout never shifts when a file gains or loses diff markers
//   - Immediate updates: polls .git/HEAD and .git/index every 500ms for branch
//     and staged-change detection; OnSave triggers an immediate re-diff
//   - Inline blame: toggle per-buffer end-of-line annotations (short hash,
//     author, relative date) via the Command menu or alt+b
//   - Blame details: a popup with the full commit (hash/author/date/summary)
//     for the line under the cursor, reachable from the Command menu; selecting
//     it opens the full commit diff
//   - Inline diff (toggled by the git.diff action): removed lines are drawn in
//     place as extra red rows labelled with their pre-change line numbers, and
//     added lines are tinted green, with the runes that actually differ picked
//     out brighter on both sides. Driven by the same live-buffer diff as the
//     gutter markers, so it works on unsaved changes.
//   - Hunk navigation: alt+n / alt+p jump the cursor to the next/previous
//     changed hunk, using the same diff data that drives the gutter markers
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/indiejames/indigo/sdk"
)

// lineKind classifies how a line differs from HEAD.
type lineKind int

const (
	lineUnchanged lineKind = iota
	lineAdded              // new line relative to HEAD (green │)
	lineDeleted            // deletion point: a red ▾ marker on this line
)

// bufState holds per-buffer diff and path.
type bufState struct {
	path       string
	lines      map[int]lineKind // 1-indexed line number → kind
	hunkStarts []int            // 1-indexed first line of each hunk, ascending

	// Inline diff (toggled by the git.diff action). removed/emph come from
	// the same diff run that drives the gutter markers, so the inline view is
	// always consistent with them and costs no extra git invocation.
	inlineDiff bool
	removed    map[int][]removedLine // 1-indexed anchor line → lines deleted above it
	emph       map[int][2]int        // 1-indexed line → rune range that differs

	blaming bool
	blame   map[int]blameLine // 1-indexed line number → blame info
}

// blameLine holds the commit that last touched one line, parsed from
// `git blame --porcelain`.
type blameLine struct {
	hash    string
	author  string
	when    time.Time
	summary string
}

// isZeroHash reports whether hash is git's all-zero placeholder used for
// uncommitted working-tree changes ("Not Committed Yet").
func isZeroHash(hash string) bool {
	return strings.Trim(hash, "0") == ""
}

// GitPlugin is the plugin state.
type GitPlugin struct {
	mu       sync.RWMutex
	api      *sdk.Api
	branch   string              // current branch name
	repoRoot string              // absolute path to the git repo root
	bufs     map[uint32]bufState // bufID → state

	// debounce: cancel pending change timers before replacing them
	timerMu sync.Mutex
	timers  map[uint32]*time.Timer
}

func (g *GitPlugin) Init(api *sdk.Api) sdk.Info {
	g.api = api
	g.bufs = map[uint32]bufState{}
	g.timers = map[uint32]*time.Timer{}

	api.HandleBufferEvents(sdk.BufferHandlers{ //nolint:errcheck
		OnOpen:   g.onOpen,
		OnChange: g.onChange,
		OnSave:   g.onSave,
		OnClose:  g.onClose,
	})
	api.Decorations(g.decorations) //nolint:errcheck

	api.OnKey("alt+b", g.onToggleBlame)                     //nolint:errcheck
	api.OnKey("alt+n", g.onNextHunk)                        //nolint:errcheck
	api.OnKey("alt+p", g.onPrevHunk)                        //nolint:errcheck
	api.OnMenuAction("git.blame", g.onToggleBlame)          //nolint:errcheck
	api.OnMenuAction("git.blame_details", g.onBlameDetails) //nolint:errcheck
	api.OnMenuAction("git.diff", g.onDiff)                  //nolint:errcheck

	go g.pollLoop()

	return sdk.Info{Name: "indigo-git", Version: "0.2.0"}
}

func (g *GitPlugin) onOpen(bufID uint32, path string) {
	root := findRepoRoot(path)
	gitLog("onOpen bufID=%d path=%s root=%s", bufID, path, root)
	g.mu.Lock()
	if g.repoRoot == "" && root != "" {
		g.repoRoot = root
	}
	g.bufs[bufID] = bufState{path: path, lines: map[int]lineKind{}}
	g.mu.Unlock()

	if root != "" {
		g.updateBranch(root)
		g.updateDiff(bufID, path, root)
	}
}

// onChange debounces keystroke events and re-diffs the buffer content 300ms
// after the last change, so gutter markers update while editing without
// running git on every single keystroke.
func (g *GitPlugin) onChange(bufID uint32, path string) {
	g.timerMu.Lock()
	if t, ok := g.timers[bufID]; ok {
		t.Stop()
	}
	g.timers[bufID] = time.AfterFunc(300*time.Millisecond, func() {
		g.mu.RLock()
		root := g.repoRoot
		g.mu.RUnlock()
		if root != "" {
			g.updateDiffFromBuffer(bufID, path, root)
		}
	})
	g.timerMu.Unlock()
}

func (g *GitPlugin) onSave(bufID uint32, path string) {
	// Cancel any pending debounced update — the saved file is now on disk.
	g.timerMu.Lock()
	if t, ok := g.timers[bufID]; ok {
		t.Stop()
		delete(g.timers, bufID)
	}
	g.timerMu.Unlock()

	g.mu.RLock()
	root := g.repoRoot
	g.mu.RUnlock()
	if root == "" {
		root = findRepoRoot(path)
		if root != "" {
			g.mu.Lock()
			g.repoRoot = root
			g.mu.Unlock()
		}
	}
	if root != "" {
		g.updateDiff(bufID, path, root)
		g.refreshBlameIfEnabled(bufID, path, root)
	}
}

func (g *GitPlugin) onClose(bufID uint32, _ string) {
	g.timerMu.Lock()
	if t, ok := g.timers[bufID]; ok {
		t.Stop()
		delete(g.timers, bufID)
	}
	g.timerMu.Unlock()

	g.mu.Lock()
	delete(g.bufs, bufID)
	g.mu.Unlock()
}

// pollLoop polls .git/HEAD and .git/index every 500ms so branch changes and
// staged changes (git add, git checkout, etc.) are picked up immediately.
func (g *GitPlugin) pollLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastHead, lastIndex string

	for range ticker.C {
		g.mu.RLock()
		root := g.repoRoot
		bufs := make(map[uint32]bufState, len(g.bufs))
		for k, v := range g.bufs {
			bufs[k] = v
		}
		g.mu.RUnlock()

		if root == "" {
			continue
		}

		// Check branch change.
		headPath := filepath.Join(root, ".git", "HEAD")
		headData, _ := os.ReadFile(headPath)
		headStr := string(headData)
		if headStr != lastHead {
			lastHead = headStr
			g.updateBranch(root)
			// Branch changed → re-diff all open buffers.
			for bufID, bs := range bufs {
				g.updateDiff(bufID, bs.path, root)
				g.refreshBlameIfEnabled(bufID, bs.path, root)
			}
		}

		// Check index change (git add, git reset, etc.).
		indexPath := filepath.Join(root, ".git", "index")
		indexInfo, err := os.Stat(indexPath)
		var indexStr string
		if err == nil {
			indexStr = fmt.Sprintf("%d", indexInfo.ModTime().UnixNano())
		}
		if indexStr != lastIndex {
			lastIndex = indexStr
			for bufID, bs := range bufs {
				g.updateDiff(bufID, bs.path, root)
				g.refreshBlameIfEnabled(bufID, bs.path, root)
			}
		}
	}
}

// refreshBlameIfEnabled recomputes blame for bufID only if blame annotations
// are currently toggled on for it — blame is comparatively expensive
// (a full `git blame` run) so it's skipped for the common case of a buffer
// nobody is viewing blame for.
func (g *GitPlugin) refreshBlameIfEnabled(bufID uint32, path, root string) {
	g.mu.RLock()
	bs, ok := g.bufs[bufID]
	g.mu.RUnlock()
	if !ok || !bs.blaming {
		return
	}
	g.computeBlame(bufID, path, root)
	g.api.RefreshDecorations(bufID)
}

// updateBranch reads .git/HEAD and stores the branch name.
func (g *GitPlugin) updateBranch(root string) {
	data, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return
	}
	s := strings.TrimSpace(string(data))
	var branch string
	if strings.HasPrefix(s, "ref: refs/heads/") {
		branch = strings.TrimPrefix(s, "ref: refs/heads/")
	} else if len(s) >= 7 {
		branch = s[:7] // detached HEAD — show short hash
	} else {
		branch = s
	}
	g.mu.Lock()
	g.branch = branch
	g.mu.Unlock()
}

// clearLines zeroes the diff state for a buffer (no gutter markers shown).
func (g *GitPlugin) clearLines(bufID uint32) {
	g.mu.Lock()
	if bs, ok := g.bufs[bufID]; ok {
		bs.lines = map[int]lineKind{}
		bs.hunkStarts = nil
		g.bufs[bufID] = bs
	}
	g.mu.Unlock()
}

// isTracked returns true if path is tracked by git (committed or staged).
// Files that are not in the index (untracked, inside .git/, temp files) return false.
func isTracked(root, path string) bool {
	out, err := runGit(root, "ls-files", "--", path)
	return err == nil && strings.TrimSpace(out) != ""
}

// updateDiffFromBuffer reads the in-memory buffer content and diffs it against
// HEAD, so gutter markers reflect unsaved edits.
func (g *GitPlugin) updateDiffFromBuffer(bufID uint32, path, root string) {
	if !isTracked(root, path) {
		g.clearLines(bufID)
		return
	}

	content, err := g.api.ReadBuffer(bufID)
	if err != nil {
		gitLog("updateDiffFromBuffer ReadBuffer bufID=%d err=%v", bufID, err)
		return
	}

	relPath, err := filepath.Rel(root, path)
	if err != nil {
		relPath = path
	}

	// Extract the HEAD version of the file into a temp file.
	headContent, _, _, err := g.api.RunProcess("git", "-C", root, "show", "HEAD:"+relPath)
	if err != nil {
		// Staged new file not yet in HEAD — treat HEAD as empty.
		headContent = ""
	}
	origTmp, err := os.CreateTemp("", "indigo-git-orig-*")
	if err != nil {
		return
	}
	origPath := origTmp.Name()
	defer os.Remove(origPath)        //nolint:errcheck
	origTmp.WriteString(headContent) //nolint:errcheck
	_ = origTmp.Close()

	// Pipe buffer content via stdin; diff reads the HEAD temp file and stdin ("-").
	// Exit code 1 = differences found (normal); 0 = identical; 2+ = error.
	cmd := exec.Command("diff", "--unified=0", origPath, "-")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(content)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Run() //nolint:errcheck

	out := stdout.String()
	gitLog("updateDiffFromBuffer bufID=%d relPath=%s outputLen=%d", bufID, relPath, len(out))
	detail := parseDiff(out)
	lines, hunkStarts := detail.lines, detail.hunkStarts
	removedByLine, emphByLine := detail.removed, detail.emph
	gitLog("updateDiffFromBuffer bufID=%d parsed %d changed lines", bufID, len(lines))

	g.mu.Lock()
	if bs, ok := g.bufs[bufID]; ok {
		bs.lines = lines
		bs.hunkStarts = hunkStarts
		bs.removed = removedByLine
		bs.emph = emphByLine
		g.bufs[bufID] = bs
	}
	g.mu.Unlock()
}

// updateDiff runs git diff HEAD -- <path> and stores the result.
func (g *GitPlugin) updateDiff(bufID uint32, path, root string) {
	if !isTracked(root, path) {
		g.clearLines(bufID)
		return
	}
	out, err := runGit(root, "diff", "HEAD", "--unified=0", "--", path)
	gitLog("updateDiff bufID=%d path=%s root=%s err=%v outputLen=%d", bufID, path, root, err, len(out))
	if err != nil {
		// Staged new file not yet in HEAD — diff against empty to show added lines.
		out, err = runGit(root, "diff", "--unified=0", "--cached", "--", path)
		gitLog("updateDiff fallback bufID=%d err=%v outputLen=%d", bufID, err, len(out))
		if err != nil {
			g.clearLines(bufID)
			return
		}
	}
	detail := parseDiff(out)
	lines, hunkStarts := detail.lines, detail.hunkStarts
	removedByLine, emphByLine := detail.removed, detail.emph
	gitLog("updateDiff bufID=%d parsed %d changed lines", bufID, len(lines))
	g.mu.Lock()
	if bs, ok := g.bufs[bufID]; ok {
		bs.lines = lines
		bs.hunkStarts = hunkStarts
		bs.removed = removedByLine
		bs.emph = emphByLine
		g.bufs[bufID] = bs
	}
	g.mu.Unlock()
}

// -- Blame --

// computeBlame runs `git blame --porcelain` for path (blaming the working
// tree, not a specific revision, so uncommitted lines show as "Not Committed
// Yet") and stores the result. Blame reflects the file as last saved to
// disk, not unsaved buffer edits — recomputing on every keystroke would make
// an already-expensive full-file blame run constantly.
func (g *GitPlugin) computeBlame(bufID uint32, path, root string) {
	relPath, err := filepath.Rel(root, path)
	if err != nil {
		relPath = path
	}
	out, err := runGit(root, "blame", "--porcelain", "--", relPath)
	if err != nil {
		gitLog("computeBlame bufID=%d err=%v", bufID, err)
		return
	}
	blame := parseBlamePorcelain(out)
	g.mu.Lock()
	if bs, ok := g.bufs[bufID]; ok {
		bs.blame = blame
		g.bufs[bufID] = bs
	}
	g.mu.Unlock()
}

// onToggleBlame flips inline blame annotations on/off for the current
// buffer. Computing blame runs in the background so the key/menu response
// isn't held up by a `git blame` invocation.
func (g *GitPlugin) onToggleBlame(_ string, ctx sdk.KeyContext) sdk.KeyResponse {
	if ctx.Mode != "normal" {
		return sdk.KeyResponse{Handled: false}
	}

	g.mu.Lock()
	bs, ok := g.bufs[ctx.BufID]
	if !ok {
		g.mu.Unlock()
		return sdk.KeyResponse{Handled: true}
	}
	bs.blaming = !bs.blaming
	turningOn := bs.blaming
	path := bs.path
	g.bufs[ctx.BufID] = bs
	root := g.repoRoot
	g.mu.Unlock()
	if root == "" {
		root = findRepoRoot(path)
	}

	bufID, clientID := ctx.BufID, ctx.ClientID
	go func() {
		if !turningOn {
			g.api.ShowMessageTo(clientID, "Blame off") //nolint:errcheck
			g.api.RefreshDecorations(bufID)
			return
		}
		if root == "" {
			g.mu.Lock()
			if bs, ok := g.bufs[bufID]; ok {
				bs.blaming = false
				g.bufs[bufID] = bs
			}
			g.mu.Unlock()
			g.api.ShowMessageTo(clientID, "Not a git repository") //nolint:errcheck
			return
		}
		g.computeBlame(bufID, path, root)
		g.api.ShowMessageTo(clientID, "Blame on") //nolint:errcheck
		g.api.RefreshDecorations(bufID)
	}()

	return sdk.KeyResponse{Handled: true}
}

// onBlameDetails shows the full commit (hash, author, date, summary) for the
// line under the cursor in a popup; selecting it opens the full commit diff.
// Unlike the inline toggle, this blames just the one line (`git blame -L`),
// so it works even when the inline overlay is off.
func (g *GitPlugin) onBlameDetails(_ string, ctx sdk.KeyContext) sdk.KeyResponse {
	if ctx.Mode != "normal" {
		return sdk.KeyResponse{Handled: false}
	}

	path, _, _, _, _, err := g.api.BufferInfo(ctx.BufID)
	if err != nil || path == "" {
		return sdk.KeyResponse{Handled: true}
	}
	g.mu.RLock()
	root := g.repoRoot
	g.mu.RUnlock()
	if root == "" {
		root = findRepoRoot(path)
	}
	if root == "" {
		g.api.ShowMessageTo(ctx.ClientID, "Not a git repository") //nolint:errcheck
		return sdk.KeyResponse{Handled: true}
	}

	clientID := ctx.ClientID
	lineNum := int(ctx.CursorLine) + 1
	go func() {
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			relPath = path
		}
		out, err := runGit(root, "blame", "-L", fmt.Sprintf("%d,%d", lineNum, lineNum), "--porcelain", "--", relPath)
		if err != nil {
			g.api.ShowMessageTo(clientID, "git blame failed: "+err.Error()) //nolint:errcheck
			return
		}
		blame := parseBlamePorcelain(out)
		bl, ok := blame[lineNum]
		if !ok {
			g.api.ShowMessageTo(clientID, "No blame info for this line") //nolint:errcheck
			return
		}
		if isZeroHash(bl.hash) {
			g.api.ShowMessageTo(clientID, "Not committed yet") //nolint:errcheck
			return
		}
		item := sdk.PopupItem{
			Label:    fmt.Sprintf("%s  %s", bl.hash[:7], bl.author),
			Sublabel: fmt.Sprintf("%s — %s", bl.when.Format("2006-01-02 15:04"), bl.summary),
			Data:     bl.hash,
		}
		g.api.ShowPopup(fmt.Sprintf("Blame: line %d", lineNum), []sdk.PopupItem{item}, func(data string) { //nolint:errcheck
			g.openCommitDiff(root, data, clientID)
		}, nil)
	}()

	return sdk.KeyResponse{Handled: true}
}

// -- Hunk navigation --

func (g *GitPlugin) onNextHunk(_ string, ctx sdk.KeyContext) sdk.KeyResponse {
	return g.jumpHunk(ctx, true)
}

func (g *GitPlugin) onPrevHunk(_ string, ctx sdk.KeyContext) sdk.KeyResponse {
	return g.jumpHunk(ctx, false)
}

// jumpHunk moves the cursor to the next (or previous) changed hunk, wrapping
// around the buffer when the cursor is past the last (or before the first).
func (g *GitPlugin) jumpHunk(ctx sdk.KeyContext, forward bool) sdk.KeyResponse {
	if ctx.Mode != "normal" {
		return sdk.KeyResponse{Handled: false}
	}
	g.mu.RLock()
	bs, ok := g.bufs[ctx.BufID]
	g.mu.RUnlock()
	if !ok || len(bs.hunkStarts) == 0 {
		g.api.ShowMessageTo(ctx.ClientID, "No changes in this file") //nolint:errcheck
		return sdk.KeyResponse{Handled: true}
	}

	cur := int(ctx.CursorLine) + 1
	starts := bs.hunkStarts // ascending, file order
	target := -1
	if forward {
		for _, s := range starts {
			if s > cur {
				target = s
				break
			}
		}
		if target == -1 {
			target = starts[0]
		}
	} else {
		for i := len(starts) - 1; i >= 0; i-- {
			if starts[i] < cur {
				target = starts[i]
				break
			}
		}
		if target == -1 {
			target = starts[len(starts)-1]
		}
	}

	return sdk.KeyResponse{Handled: true, HasCursor: true, CursorLine: uint32(target - 1), CursorCol: 0}
}

// -- Diff view --

// onDiff toggles the inline diff for the current buffer: lines removed
// relative to HEAD are shown in place as extra rows, and added/changed lines
// are tinted, instead of opening `git diff HEAD` in a separate tab.
//
// It reuses the diff data already computed for the gutter markers, which is
// produced from the *live buffer* (see updateDiffFromBuffer) rather than from
// disk — so the inline view is correct with unsaved changes and needs no
// save first.
func (g *GitPlugin) onDiff(_ string, ctx sdk.KeyContext) sdk.KeyResponse {
	if ctx.Mode != "normal" {
		return sdk.KeyResponse{Handled: false}
	}

	g.mu.Lock()
	bs, ok := g.bufs[ctx.BufID]
	if !ok {
		g.mu.Unlock()
		return sdk.KeyResponse{Handled: true}
	}
	bs.inlineDiff = !bs.inlineDiff
	on, changed := bs.inlineDiff, len(bs.lines) > 0
	g.bufs[ctx.BufID] = bs
	g.mu.Unlock()

	switch {
	case on && !changed:
		g.api.ShowMessageTo(ctx.ClientID, "No changes vs HEAD") //nolint:errcheck
	case on:
		g.api.ShowMessageTo(ctx.ClientID, "Inline diff on") //nolint:errcheck
	default:
		g.api.ShowMessageTo(ctx.ClientID, "Inline diff off") //nolint:errcheck
	}
	// Push rather than waiting for the next poll, so the toggle feels
	// immediate instead of up to ~360ms late.
	g.api.RefreshDecorations(ctx.BufID)

	return sdk.KeyResponse{Handled: true}
}

// openCommitDiff opens `git show <hash>` for hash in a new tab (a scratch
// temp file). Used by the blame-details popup's onSelect.
func (g *GitPlugin) openCommitDiff(root, hash string, clientID uint64) {
	out, err := runGit(root, "show", hash)
	if err != nil {
		g.api.ShowMessageTo(clientID, "git show failed: "+err.Error()) //nolint:errcheck
		return
	}
	short := hash
	if len(short) > 7 {
		short = short[:7]
	}
	tmpPath, err := writeTemp(fmt.Sprintf("indigo-show-%s-*.diff", short), out)
	if err != nil {
		g.api.ShowMessageTo(clientID, "failed to write diff: "+err.Error()) //nolint:errcheck
		return
	}
	g.api.OpenFile(tmpPath, 0) //nolint:errcheck
}

// writeTemp writes content to a fresh temp file (see os.CreateTemp for the
// pattern's "*" placeholder) and returns its path.
func writeTemp(pattern, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.WriteString(content); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// blameHeaderRe matches a `git blame --porcelain` line-info header:
// "<hash> <origLine> <finalLine> [<numLines>]", where hash is 40 hex chars
// for a SHA-1 repo or 64 hex chars for a SHA-256 repo (git's experimental
// object format).
var blameHeaderRe = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64}) (\d+) (\d+)(?: \d+)?$`)

// parseBlamePorcelain parses `git blame --porcelain` output into a map of
// 1-indexed final-file line numbers to blameLine. Porcelain repeats a
// commit's full metadata (author/author-time/summary/...) only the first
// time that commit appears in the output; later lines from the same commit
// show just the header followed directly by the tab-prefixed content line.
// This walks header→[metadata]→content per line, filling in cached metadata
// for repeat hashes, so it works for both cases uniformly.
func parseBlamePorcelain(output string) map[int]blameLine {
	result := map[int]blameLine{}
	meta := map[string]*blameLine{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var curHash string
	var curFinal int
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "\t") {
			// Content line — the record for this source line ends here.
			if curHash != "" && curFinal > 0 {
				if bl, ok := meta[curHash]; ok {
					result[curFinal] = *bl
				} else {
					result[curFinal] = blameLine{hash: curHash}
				}
			}
			curHash = ""
			continue
		}
		if m := blameHeaderRe.FindStringSubmatch(line); m != nil {
			curHash = m[1]
			curFinal, _ = strconv.Atoi(m[3])
			if _, ok := meta[curHash]; !ok {
				meta[curHash] = &blameLine{hash: curHash}
			}
			continue
		}
		if curHash == "" {
			continue
		}
		bl := meta[curHash]
		key, val, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		switch key {
		case "author":
			bl.author = val
		case "author-time":
			secs, _ := strconv.ParseInt(val, 10, 64)
			bl.when = time.Unix(secs, 0)
		case "summary":
			bl.summary = val
		}
	}
	return result
}

func gitLog(format string, args ...any) {
	path := fmt.Sprintf("%s/indigo-git.log", os.TempDir())
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()                      //nolint:errcheck
	fmt.Fprintf(f, format+"\n", args...) //nolint:errcheck
}

// decorations is the pull-only callback invoked by the editor on each render frame.
func (g *GitPlugin) decorations(bufID uint32, _ uint64, r sdk.Range) []sdk.Decoration {
	g.mu.RLock()
	branch := g.branch
	bs, hasBuf := g.bufs[bufID]
	var lineDiff map[int]lineKind
	var blameData map[int]blameLine
	var removedData map[int][]removedLine
	var emphData map[int][2]int
	blaming, inlineDiff := false, false
	if hasBuf {
		lineDiff = bs.lines
		blameData = bs.blame
		blaming = bs.blaming
		inlineDiff = bs.inlineDiff
		removedData = bs.removed
		emphData = bs.emph
	}
	g.mu.RUnlock()

	var out []sdk.Decoration

	// Status bar: branch name (always, even when there are no diffs).
	if branch != "" {
		out = append(out, sdk.Decoration{
			Kind: sdk.DecorationStatusBar,
			Text: "  " + branch + "  ", // nf-pl-branch glyph; falls back gracefully
		})
	}

	// Gutter: emit one decoration per visible line for a fixed-width gutter.
	for line := r.Start.Line; line <= r.End.Line; line++ {
		ln := int(line) + 1 // decorations use 0-indexed lines; diff map uses 1-indexed
		kind := lineUnchanged
		if lineDiff != nil {
			kind = lineDiff[ln]
		}
		dec := gutterDecoration(line, kind)
		out = append(out, dec)

		if !inlineDiff {
			continue
		}
		// Removed lines are anchored above the line that replaced them, and
		// are emitted only for visible rows so a file with a huge deletion
		// doesn't ship its whole history to the client every refresh.
		for _, rl := range removedData[ln] {
			out = append(out, sdk.Decoration{
				Kind:    sdk.DecorationRemovedLine,
				Line:    line,
				Text:    rl.Text,
				Col:     uint32(rl.EmphStart),
				EndCol:  uint32(rl.EmphEnd),
				OldLine: uint32(rl.OldLine),
			})
		}
		// Whole-line tint for added/changed lines, then the narrower
		// intra-line emphasis on top of it where we know what changed.
		if bg := tintForKind(kind); bg != "" {
			out = append(out, sdk.Decoration{
				Kind:      sdk.DecorationLineTint,
				Line:      line,
				Col:       0,
				EndCol:    ^uint32(0), // whole line; the client clamps to its length
				TextColor: bg,
			})
			if e, ok := emphData[ln]; ok && e[0] < e[1] {
				out = append(out, sdk.Decoration{
					Kind:      sdk.DecorationLineTint,
					Line:      line,
					Col:       uint32(e[0]),
					EndCol:    uint32(e[1]),
					TextColor: emphTintForKind(kind),
				})
			}
		}
	}

	if blaming && len(blameData) > 0 {
		out = append(out, g.buildBlameOverlays(bufID, r, blameData)...)
	}

	return out
}

// buildBlameOverlays fetches the visible lines' text (needed to know where
// each line ends) and returns one end-of-line overlay decoration per visible
// line that has blame info.
func (g *GitPlugin) buildBlameOverlays(bufID uint32, r sdk.Range, blameData map[int]blameLine) []sdk.Decoration {
	lineTexts, err := g.api.ReadLines(bufID, r.Start.Line, r.End.Line+1)
	if err != nil {
		return nil
	}
	var out []sdk.Decoration
	for i, txt := range lineTexts {
		line := r.Start.Line + uint32(i)
		bl, ok := blameData[int(line)+1]
		if !ok {
			continue
		}
		color := "#888888"
		if isZeroHash(bl.hash) {
			color = "#CC8800"
		}
		out = append(out, sdk.Decoration{
			Kind:      sdk.DecorationOverlay,
			Line:      line,
			Col:       uint32(len([]rune(txt))),
			Text:      formatBlameOverlay(bl),
			TextColor: color,
		})
	}
	return out
}

// formatBlameOverlay renders bl as short end-of-line virtual text, e.g.
// "  a1b2c3d Jane Doe, 3 days ago".
func formatBlameOverlay(bl blameLine) string {
	if isZeroHash(bl.hash) {
		return "  uncommitted"
	}
	short := bl.hash
	if len(short) > 7 {
		short = short[:7]
	}
	author := bl.author
	if author == "" {
		author = "unknown"
	}
	return fmt.Sprintf("  %s %s, %s", short, author, relativeTime(bl.when))
}

// relativeTime renders t as a short "N units ago" string, matching the grain
// GitHub/GitLens-style blame annotations use.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "unknown time"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return pluralAgo(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return pluralAgo(int(d.Hours()), "hour")
	case d < 30*24*time.Hour:
		return pluralAgo(int(d.Hours()/24), "day")
	case d < 365*24*time.Hour:
		return pluralAgo(int(d.Hours()/24/30), "month")
	default:
		return pluralAgo(int(d.Hours()/24/365), "year")
	}
}

func pluralAgo(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s ago", unit)
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

func gutterDecoration(line uint32, kind lineKind) sdk.Decoration {
	switch kind {
	case lineAdded:
		return sdk.Decoration{Kind: sdk.DecorationGutter, Line: line, Text: "+", TextColor: gitAddedFg}
	case lineDeleted:
		return sdk.Decoration{Kind: sdk.DecorationGutter, Line: line, Text: "-", TextColor: gitRemovedFg}
	default:
		return sdk.Decoration{Kind: sdk.DecorationGutter, Line: line, Text: ""}
	}
}

// parseDiff parses the output of `git diff --unified=0` and returns a map of
// 1-indexed new-file line numbers to their lineKind, plus the 1-indexed first
// line of each hunk in file order (for hunk-navigation commands).
// removedLine is a line present in HEAD but not in the buffer, along with the
// rune range within it that actually differs from the line that replaced it
// (empty for a pure deletion, which has no counterpart).
type removedLine struct {
	Text      string
	EmphStart int
	EmphEnd   int
	// OldLine is this content's 1-based line number in the pre-change file,
	// shown in the gutter beside it the way `git diff` numbers removals.
	OldLine int
}

// diffDetail is everything parseDiff extracts from one diff run.
//
// lines/hunkStarts drive the gutter markers and hunk navigation, and are what
// this parser produced originally. removed and emph were added for the inline
// diff: the `-`/`+` content lines were previously skipped entirely (only the
// @@ headers were read), so the removed text and the intra-line ranges had to
// be recovered before anything could be shown in place.
type diffDetail struct {
	lines      map[int]lineKind
	hunkStarts []int
	// removed maps a 1-based new-file line to the lines deleted immediately
	// above it, in order.
	removed map[int][]removedLine
	// emph maps a 1-based new-file line to the rune range on it that differs
	// from the line it replaced.
	emph map[int][2]int
}

// parseDiff extracts gutter kinds, hunk starts, removed content, and
// intra-line ranges from `diff --unified=0` output.
//
// --unified=0 means a hunk body is exactly its `-` lines followed by its `+`
// lines, with no context, so the two sides can be paired positionally.
func parseDiff(output string) diffDetail {
	d := diffDetail{
		lines:   map[int]lineKind{},
		removed: map[int][]removedLine{},
		emph:    map[int][2]int{},
	}

	var (
		curMark  int      // anchor line for the hunk's removed rows
		curOld   int      // pre-change line number of the hunk's first removed line
		oldTexts []string // `-` lines of the current hunk
		newTexts []string // `+` lines of the current hunk
		inHunk   bool
	)

	// flush pairs up the current hunk's removed and added lines. Positional
	// pairing: the Nth removed line corresponds to the Nth added line, which
	// is what a --unified=0 hunk gives us and is right for the common case of
	// an edit in place. Surplus removed lines have no counterpart and carry
	// no emphasis.
	flush := func() {
		if !inHunk {
			return
		}
		for i, oldText := range oldTexts {
			rl := removedLine{Text: oldText, OldLine: curOld + i}
			if i < len(newTexts) {
				oldR, newR := intraLineDiff(oldText, newTexts[i])
				rl.EmphStart, rl.EmphEnd = oldR[0], oldR[1]
				if newR[0] < newR[1] {
					d.emph[curMark+i] = newR
				}
			}
			d.removed[curMark] = append(d.removed[curMark], rl)
		}
		oldTexts, newTexts, inHunk = nil, nil, false
	}

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), maxDiffLineBytes)
	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "@@"):
			flush()
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			continue // file headers, not content
		case inHunk && strings.HasPrefix(line, "-"):
			oldTexts = append(oldTexts, line[1:])
			continue
		case inHunk && strings.HasPrefix(line, "+"):
			newTexts = append(newTexts, line[1:])
			continue
		default:
			continue
		}

		// Parse: @@ -oldStart[,oldCount] +newStart[,newCount] @@
		oldStart, oldCount, newStart, newCount := parseHunkHeader(line)
		if newCount == 0 && oldCount == 0 {
			continue
		}
		// newStart is the first line of the new file the hunk touches; for a
		// deletion at the very start of the file it may be 0 — clamp to 1.
		mark := newStart
		if mark < 1 {
			mark = 1
		}
		d.hunkStarts = append(d.hunkStarts, mark)
		// Removed rows render *above* the line they anchor to, which is not
		// the same line the gutter marker goes on:
		//
		//   - Mixed hunk: newStart is the line that replaced the removed
		//     content, so the removed rows belong directly above it.
		//   - Pure deletion: "@@ -2 +1,0 @@" means zero lines at new-file
		//     position 1 — the removed content sat *after* new line 1, so it
		//     belongs above line newStart+1. Anchoring at newStart put it a
		//     line too high, above the deletion point rather than at it.
		//
		// newStart can be 0 for a deletion at the very start of the file,
		// which lands correctly on line 1 here.
		curMark = mark
		if newCount == 0 {
			curMark = newStart + 1
		}
		curOld = oldStart
		inHunk = true
		switch {
		case oldCount == 0:
			// Pure addition: newStart..newStart+newCount are ADDED.
			for i := 0; i < newCount; i++ {
				d.lines[newStart+i] = lineAdded
			}
		case newCount == 0:
			// Pure deletion: mark the line at newStart as a deletion point.
			// Only mark if not already a stronger decoration.
			if d.lines[mark] == lineUnchanged {
				d.lines[mark] = lineDeleted
			}
		default:
			// Mixed hunk: every new line is an ADDITION, and the lines it
			// replaced are shown separately as removed rows.
			//
			// This is git's model, deliberately: git has no "modified line"
			// concept at all, only `-` and `+`. Earlier versions marked these
			// lines CHANGED and rendered them blue, which made a real edit
			// ("@@ -1,2 +1,6 @@") show six blue lines where git showed two
			// removals and six additions.
			//
			// The -/+ pairing still happens in flush() for *intra-line*
			// emphasis — the one place indigo is more precise than git, since
			// it can point at the runes that actually differ. It just no
			// longer changes the line's colour.
			for i := 0; i < newCount; i++ {
				d.lines[newStart+i] = lineAdded
			}
		}
	}
	flush()
	return d
}

// maxDiffLineBytes caps a single diff line so a minified or generated file
// can't make the scanner allocate without bound.
const maxDiffLineBytes = 1 << 20

// intraLineDiff returns the rune ranges of oldS and newS that actually
// differ, found by trimming the common prefix and the common suffix.
//
// This is deliberately not a full LCS. The dominant real edit is a change
// somewhere inside an otherwise-identical line, which prefix/suffix trimming
// isolates exactly; multi-region edits degrade to one span covering all of
// them, which is still far better than tinting the whole line. Both returned
// ranges are half-open [start, end) in runes, and are empty when the strings
// are identical.
func intraLineDiff(oldS, newS string) (oldRange, newRange [2]int) {
	o, n := []rune(oldS), []rune(newS)
	p := 0
	for p < len(o) && p < len(n) && o[p] == n[p] {
		p++
	}
	suf := 0
	for suf < len(o)-p && suf < len(n)-p && o[len(o)-1-suf] == n[len(n)-1-suf] {
		suf++
	}
	return [2]int{p, len(o) - suf}, [2]int{p, len(n) - suf}
}

// parseHunkHeader extracts oldCount, newStart, newCount from a @@ line.
func parseHunkHeader(line string) (oldStart, oldCount, newStart, newCount int) {
	// Format: @@ -A[,B] +C[,D] @@ ...
	end := strings.Index(line[2:], "@@")
	if end < 0 {
		return
	}
	chunk := strings.TrimSpace(line[2 : end+2])
	parts := strings.Fields(chunk)
	if len(parts) < 2 {
		return
	}
	oldStart, oldCount = parseRange(parts[0]) // parts[0] starts with '-'
	newStart, newCount = parseRange(parts[1])
	return
}

// parseRange parses "-A,B" or "+A,B" into (start, count).
// If count is omitted it defaults to 1.
func parseRange(s string) (start, count int) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	if idx := strings.Index(s, ","); idx >= 0 {
		start, _ = strconv.Atoi(s[:idx])
		count, _ = strconv.Atoi(s[idx+1:])
	} else {
		start, _ = strconv.Atoi(s)
		count = 1
	}
	return
}

// findRepoRoot walks up from path looking for a .git directory.
func findRepoRoot(path string) string {
	dir := filepath.Dir(path)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// runGit runs a git command in the given working directory and returns stdout.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %v: %w: %s", args, err, stderr.String())
	}
	return stdout.String(), nil
}

func main() {
	if err := sdk.Run(&GitPlugin{}); err != nil {
		fmt.Fprintf(os.Stderr, "indigo-git: %v\n", err)
		os.Exit(1)
	}
}

// tintForKind returns the whole-line background for a diff kind, or "" for
// kinds that get no tint. Deliberately dark: the tint sits behind syntax
// highlighting, so it has to read as a wash rather than compete with the
// foreground colours on top of it.
func tintForKind(k lineKind) string {
	switch k {
	case lineAdded:
		return "#16301C"
	default:
		return ""
	}
}

// emphTintForKind returns the brighter background marking the runes that
// actually changed within a line, layered over tintForKind's wash.
func emphTintForKind(k lineKind) string {
	switch k {
	case lineAdded:
		return "#245C31"
	default:
		return ""
	}
}

// Accent colours for the diff gutter, matching git's own red/green. These are
// foregrounds for the +/- signs and the line numbers beside them, distinct
// from the darker line backgrounds in tintForKind.
const (
	gitAddedFg   = "#44BB44"
	gitRemovedFg = "#FF5555"
)
