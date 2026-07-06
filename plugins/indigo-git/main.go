// indigo-git is a plugin for the indigo editor that provides git integration.
//
// Features:
//   - Current branch name in the status bar
//   - Gutter decorations showing git diff output:
//     green │ for added lines, blue │ for changed lines, red ▾ for deletion points
//   - Fixed-width gutter: always emits a decoration for every visible line so
//     layout never shifts when a file gains or loses diff markers
//   - Immediate updates: polls .git/HEAD and .git/index every 500ms for branch
//     and staged-change detection; OnSave triggers an immediate re-diff
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	lineAdded              // new line not in HEAD (green │)
	lineChanged            // line that replaced a deleted line (blue │)
	lineDeleted            // deletion point: a red ▾ marker on this line
)

// bufState holds per-buffer diff and path.
type bufState struct {
	path  string
	lines map[int]lineKind // 1-indexed line number → kind
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

	go g.pollLoop()

	return sdk.Info{Name: "indigo-git", Version: "0.1.0"}
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
			}
		}
	}
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

// updateDiffFromBuffer reads the in-memory buffer content and diffs it against
// HEAD, so gutter markers reflect unsaved edits.
func (g *GitPlugin) updateDiffFromBuffer(bufID uint32, path, root string) {
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
		// File not tracked in HEAD (new file) — treat HEAD as empty.
		headContent = ""
	}
	origTmp, err := os.CreateTemp("", "indigo-git-orig-*")
	if err != nil {
		return
	}
	origPath := origTmp.Name()
	defer os.Remove(origPath) //nolint:errcheck
	origTmp.WriteString(headContent) //nolint:errcheck
	_ = origTmp.Close()

	// Write buffer content to a second temp file.
	bufTmp, err := os.CreateTemp("", "indigo-git-buf-*")
	if err != nil {
		return
	}
	bufPath := bufTmp.Name()
	defer os.Remove(bufPath) //nolint:errcheck
	bufTmp.WriteString(content) //nolint:errcheck
	_ = bufTmp.Close()

	// Diff the two temp files. Exit code 1 = differences found (normal).
	cmd := exec.Command("git", "diff", "--unified=0", "--no-index", "--", origPath, bufPath)
	cmd.Dir = root
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Run() //nolint:errcheck

	out := stdout.String()
	gitLog("updateDiffFromBuffer bufID=%d relPath=%s outputLen=%d", bufID, relPath, len(out))
	lines := parseDiff(out)
	gitLog("updateDiffFromBuffer bufID=%d parsed %d changed lines", bufID, len(lines))

	g.mu.Lock()
	if bs, ok := g.bufs[bufID]; ok {
		bs.lines = lines
		g.bufs[bufID] = bs
	}
	g.mu.Unlock()
}

// updateDiff runs git diff HEAD -- <path> and stores the result.
func (g *GitPlugin) updateDiff(bufID uint32, path, root string) {
	out, err := runGit(root, "diff", "HEAD", "--unified=0", "--", path)
	gitLog("updateDiff bufID=%d path=%s root=%s err=%v outputLen=%d", bufID, path, root, err, len(out))
	if err != nil {
		// Might be a new file not yet committed — try diff against empty tree.
		out, err = runGit(root, "diff", "--unified=0", "--", path)
		gitLog("updateDiff fallback bufID=%d err=%v outputLen=%d", bufID, err, len(out))
		if err != nil {
			g.mu.Lock()
			if bs, ok := g.bufs[bufID]; ok {
				bs.lines = map[int]lineKind{}
				g.bufs[bufID] = bs
			}
			g.mu.Unlock()
			return
		}
	}
	lines := parseDiff(out)
	gitLog("updateDiff bufID=%d parsed %d changed lines", bufID, len(lines))
	g.mu.Lock()
	if bs, ok := g.bufs[bufID]; ok {
		bs.lines = lines
		g.bufs[bufID] = bs
	}
	g.mu.Unlock()
}

func gitLog(format string, args ...any) {
	path := fmt.Sprintf("%s/indigo-git.log", os.TempDir())
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	fmt.Fprintf(f, format+"\n", args...) //nolint:errcheck
}

// decorations is the pull-only callback invoked by the editor on each render frame.
func (g *GitPlugin) decorations(bufID uint32, _ uint64, r sdk.Range) []sdk.Decoration {
	g.mu.RLock()
	branch := g.branch
	bs, hasBuf := g.bufs[bufID]
	var lineDiff map[int]lineKind
	if hasBuf {
		lineDiff = bs.lines
	}
	g.mu.RUnlock()

	var out []sdk.Decoration

	// Status bar: branch name (always, even when there are no diffs).
	if branch != "" {
		out = append(out, sdk.Decoration{
			Kind: sdk.DecorationStatusBar,
			Text: "  " + branch, // nf-pl-branch glyph; falls back gracefully
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
	}

	return out
}

func gutterDecoration(line uint32, kind lineKind) sdk.Decoration {
	switch kind {
	case lineAdded:
		return sdk.Decoration{Kind: sdk.DecorationGutter, Line: line, Text: "│", TextColor: "#44BB44"}
	case lineChanged:
		return sdk.Decoration{Kind: sdk.DecorationGutter, Line: line, Text: "│", TextColor: "#4488FF"}
	case lineDeleted:
		return sdk.Decoration{Kind: sdk.DecorationGutter, Line: line, Text: "▾", TextColor: "#FF5555"}
	default:
		return sdk.Decoration{Kind: sdk.DecorationGutter, Line: line, Text: ""}
	}
}

// parseDiff parses the output of `git diff --unified=0` and returns a map of
// 1-indexed new-file line numbers to their lineKind.
func parseDiff(output string) map[int]lineKind {
	result := map[int]lineKind{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		// Parse: @@ -oldStart[,oldCount] +newStart[,newCount] @@
		oldCount, newStart, newCount := parseHunkHeader(line)
		if newCount == 0 && oldCount == 0 {
			continue
		}
		switch {
		case oldCount == 0:
			// Pure addition: newStart..newStart+newCount are ADDED.
			for i := 0; i < newCount; i++ {
				result[newStart+i] = lineAdded
			}
		case newCount == 0:
			// Pure deletion: mark the line at newStart as a deletion point.
			// newStart is the first line of the new file after the deletion.
			// If deletion is at start of file, newStart may be 0 — clamp to 1.
			mark := newStart
			if mark < 1 {
				mark = 1
			}
			// Only mark if not already a stronger decoration.
			if result[mark] == lineUnchanged {
				result[mark] = lineDeleted
			}
		default:
			// Mixed hunk: newStart..newStart+newCount are CHANGED.
			for i := 0; i < newCount; i++ {
				result[newStart+i] = lineChanged
			}
		}
	}
	return result
}

// parseHunkHeader extracts oldCount, newStart, newCount from a @@ line.
func parseHunkHeader(line string) (oldCount, newStart, newCount int) {
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
	_, oldCount = parseRange(parts[0]) // parts[0] starts with '-'
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
