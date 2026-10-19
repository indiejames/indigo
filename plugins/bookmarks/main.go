// Bookmarks plugin for indigo.
//
// Bookmarks persist across sessions in ~/.config/indigo/plugins/bookmarks/bookmarks.json.
// Line numbers are automatically updated as the buffer is edited.
//
// Both actions are under Bookmarks in the Command menu:
//
//	space b b  toggle a bookmark on the current line (prompts for a name if adding)
//	space b l  list bookmarks (Enter to jump, Esc to dismiss)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/indiejames/indigo/sdk"
)

// savedBookmark is the on-disk format.
type savedBookmark struct {
	FilePath string `json:"filePath"`
	Line     uint32 `json:"line"`
	Col      uint32 `json:"col"`
	Note     string `json:"note,omitempty"`
}

// bookmark is the in-memory form.
type bookmark struct {
	filePath string
	line     uint32
	col      uint32
	note     string
	active   bool
}

// Bookmarks is the plugin state.
type Bookmarks struct {
	mu        sync.Mutex
	api       *sdk.Api
	bookmarks []bookmark

	// persistCh serializes bookmark writes onto a single worker goroutine
	// (persistWorker) — see enqueuePersist's doc comment for why a snapshot
	// copy alone isn't enough to keep bookmarks.json correct under
	// concurrent mutations.
	persistCh chan []bookmark
}

func (b *Bookmarks) Init(api *sdk.Api) sdk.Info {
	b.api = api
	b.bookmarks = loadBookmarks()
	b.persistCh = make(chan []bookmark, 64)
	go b.persistWorker()

	api.OnMenuAction("bookmarks.toggle", b.onToggle) //nolint:errcheck
	api.OnMenuAction("bookmarks.open", b.onList)     //nolint:errcheck
	api.OnEdit(b.onEdit)                             //nolint:errcheck
	api.Decorations(b.getDecorations)                //nolint:errcheck

	return sdk.Info{Name: "bookmarks", Version: "0.3.0"}
}

// persistWorker drains persistCh and writes each snapshot to disk one at a
// time, for the life of the plugin process (never torn down explicitly —
// the process exits as a whole when the server kills the plugin, same as
// any other background goroutine in this plugin).
func (b *Bookmarks) persistWorker() {
	for snapshot := range b.persistCh {
		persistBookmarks(snapshot)
	}
}

// enqueuePersist queues bmarks to be written by the single persistWorker
// goroutine. Caller must hold b.mu and call this *before* unlocking — the
// enqueue must happen inside the same critical section as the mutation it's
// persisting, or a second, concurrent mutation could enqueue its own (newer)
// snapshot first, landing out of order.
//
// This exists because a snapshot copy alone (see snapshotBookmarks) only
// fixes the *data race* on b.bookmarks — it does nothing to order the actual
// disk writes. onToggle's add/remove paths and onEdit's line-shift each
// call persistBookmarks from their own goroutine (directly, or via `go`);
// those file writes have no ordering guarantee relative to each other even
// though their snapshots were taken in the correct mutation order under
// b.mu — an older snapshot's write landing on disk *after* a newer one's
// would silently revert bookmarks.json to stale state. Routing every write
// through one worker goroutine, fed in mutation order, guarantees the file
// always reflects whichever snapshot was taken last.
//
// The send is unconditional (no "channel full, write directly" fallback):
// a fallback like that would let a write bypass the queue and race the
// worker's own writes under exactly the high-contention conditions this
// exists to guard against — deterministic ordering only holds if *every*
// write goes through the single FIFO worker, no exceptions. Blocking here is
// safe: onToggle/onEdit already run on their own dispatched goroutine
// (the plugin manager calls InvokeMenuAction/DispatchEditEvent independently per
// invocation), and persistWorker never does anything but drain this channel,
// so it always eventually catches up.
func (b *Bookmarks) enqueuePersist(bmarks []bookmark) {
	b.persistCh <- snapshotBookmarks(bmarks)
}

// onToggle toggles a bookmark on the current line.
// If a bookmark already exists at this line it is removed; otherwise an input
// prompt is shown to collect a name before adding it.
func (b *Bookmarks) onToggle(_ string, ctx sdk.KeyContext) sdk.KeyResponse {
	if ctx.Mode != "normal" {
		return sdk.KeyResponse{Handled: false}
	}

	filePath, _, _, _, _, err := b.api.BufferInfo(ctx.BufID)
	if err != nil {
		return sdk.KeyResponse{Handled: true}
	}

	if b.removeBookmarkAt(filePath, ctx.CursorLine) {
		return sdk.KeyResponse{Handled: true}
	}

	// Not bookmarked yet — prompt for a name.
	fp := filePath
	line := ctx.CursorLine
	col := ctx.CursorCol
	b.api.ShowInputPrompt("Bookmark name (optional)", "", func(note string) { //nolint:errcheck
		b.mu.Lock()
		b.bookmarks = append(b.bookmarks, bookmark{
			filePath: fp,
			line:     line,
			col:      col,
			note:     note,
			active:   true,
		})
		b.enqueuePersist(b.bookmarks)
		b.mu.Unlock()
	}, nil)

	return sdk.KeyResponse{Handled: true}
}

// onList shows the bookmark picker popup.
func (b *Bookmarks) onList(_ string, ctx sdk.KeyContext) sdk.KeyResponse {
	if ctx.Mode != "normal" {
		return sdk.KeyResponse{Handled: false}
	}

	b.mu.Lock()
	var items []sdk.PopupItem
	for _, bm := range b.bookmarks {
		if !bm.active {
			continue
		}
		label := filepath.Base(bm.filePath)
		if label == "" || label == "." {
			label = bm.filePath
		}
		label += fmt.Sprintf(":%d", bm.line+1)
		sublabel := bm.note
		data := fmt.Sprintf("%s\x00%d", bm.filePath, bm.line)
		items = append(items, sdk.PopupItem{Label: label, Sublabel: sublabel, Data: data})
	}
	b.mu.Unlock()

	if len(items) == 0 {
		b.api.ShowMessageTo(ctx.ClientID, "No bookmarks") //nolint:errcheck
		return sdk.KeyResponse{Handled: true}
	}

	b.api.ShowPopup("Bookmarks", items, func(data string) { //nolint:errcheck
		fp, lineStr, _ := strings.Cut(data, "\x00")
		var line uint32
		fmt.Sscanf(lineStr, "%d", &line) //nolint:errcheck
		b.api.OpenFile(fp, line)         //nolint:errcheck
	}, nil)

	return sdk.KeyResponse{Handled: true}
}

// onEdit adjusts bookmark line numbers when lines are inserted or deleted.
func (b *Bookmarks) onEdit(e sdk.EditEvent) {
	if e.LineDelta == 0 || e.FilePath == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if shiftBookmarks(b.bookmarks, e) {
		b.enqueuePersist(b.bookmarks)
	}
}

// shiftBookmarks moves or suspends the active bookmarks in bmarks on e's file
// to follow e, and reports whether any changed. A bookmark follows its line's
// text, using the rules sdk.EditEvent documents:
//   - An insertion moves its line's text from FromCol on down, so a bookmark on
//     FromLine moves with it only when FromCol is 0: opening a line above a
//     bookmarked line keeps the bookmark on that line's text, while pressing
//     Enter at its end leaves it.
//   - A deletion joins FromLine's head to ToLine's tail. Lines strictly between
//     are gone, and FromLine is too when FromCol is 0. ToLine's bookmark moves
//     up to the joined line, unless FromLine kept its own there: joining two
//     lines (J, or backspace at the start of a line) keeps the first line's
//     bookmark rather than suspending it, as the line-only event used to.
//
// Without a range (an editor older than these fields), FromLine and LineDelta
// are all there is, and the old line-only rule applies.
func shiftBookmarks(bmarks []bookmark, e sdk.EditEvent) bool {
	changed := false
	keptFromLine := false
	if e.LineDelta < 0 && e.HasRange && e.FromCol > 0 {
		for _, bm := range bmarks {
			if bm.active && bm.filePath == e.FilePath && bm.line == e.FromLine {
				keptFromLine = true
			}
		}
	}
	for i := range bmarks {
		bm := &bmarks[i]
		if bm.filePath != e.FilePath || !bm.active {
			continue
		}
		before := *bm
		switch {
		case !e.HasRange:
			shiftLineOnly(bm, e.FromLine, e.LineDelta)
		case e.LineDelta > 0:
			if bm.line > e.FromLine || (bm.line == e.FromLine && e.FromCol == 0) {
				bm.line += uint32(e.LineDelta)
			}
		default:
			switch {
			case bm.line > e.ToLine:
				bm.line = uint32(int32(bm.line) + e.LineDelta)
			case bm.line == e.ToLine:
				if keptFromLine {
					bm.active = false
				} else {
					bm.line = e.FromLine
				}
			case bm.line > e.FromLine || (bm.line == e.FromLine && e.FromCol == 0):
				bm.active = false
			}
		}
		if *bm != before {
			changed = true
		}
	}
	return changed
}

// shiftLineOnly is the rule for an event with no range: lines from atLine on
// are taken to be deleted, and insertions to come after atLine.
func shiftLineOnly(bm *bookmark, atLine uint32, lineDelta int32) {
	if lineDelta < 0 {
		deletedTo := atLine + uint32(-lineDelta)
		if bm.line >= deletedTo {
			bm.line = uint32(int32(bm.line) + lineDelta)
		} else if bm.line >= atLine {
			bm.active = false
		}
	} else if bm.line > atLine {
		bm.line += uint32(lineDelta)
	}
}

// getDecorations returns left-gutter decorations for bookmarks in the current buffer.
func (b *Bookmarks) getDecorations(bufID uint32, _ uint64, _ sdk.Range) []sdk.Decoration {
	path, _, _, _, _, err := b.api.BufferInfo(bufID)
	if err != nil || path == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var decorations []sdk.Decoration
	for _, bm := range b.bookmarks {
		if bm.active && bm.filePath == path {
			decorations = append(decorations, sdk.Decoration{
				Line:      bm.line,
				Col:       0,
				Text:      "▶",
				Kind:      sdk.DecorationLeftGutter,
				TextColor: "#5588FF",
			})
		}
	}
	return decorations
}

// snapshotBookmarks returns an independent copy of bmarks. Caller must hold
// b.mu. persistBookmarks does file I/O and must never be called on b.bookmarks
// directly outside the lock: onEdit can run concurrently with onToggle
// (the plugin manager dispatches InvokeMenuAction and DispatchEditEvent as
// independent goroutines) and mutates bookmark elements in place via pointer,
// so an unlocked persistBookmarks call ranging over the live slice would read
// torn fields or race append's slice-header/backing-array writes. A snapshot
// is unaffected by any later mutation, so it's safe to read — and persist —
// after releasing the lock.
func snapshotBookmarks(bmarks []bookmark) []bookmark {
	return append([]bookmark(nil), bmarks...)
}

// removeBookmarkAt removes the active bookmark at (filePath, line), if any, and
// reports whether it did. The lookup and the removal share one critical
// section: onToggle used to find the index, unlock, then lock again and remove by
// that index, so a concurrent onEdit shift or another removal in between
// could make it delete a different bookmark or slice out of range.
func (b *Bookmarks) removeBookmarkAt(filePath string, line uint32) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	idx := b.findBookmark(filePath, line)
	if idx < 0 {
		return false
	}
	b.bookmarks = append(b.bookmarks[:idx], b.bookmarks[idx+1:]...)
	b.enqueuePersist(b.bookmarks)
	return true
}

// findBookmark returns the index of an active bookmark at (filePath, line), or -1.
// Caller must hold b.mu.
func (b *Bookmarks) findBookmark(filePath string, line uint32) int {
	for i, bm := range b.bookmarks {
		if bm.active && bm.filePath == filePath && bm.line == line {
			return i
		}
	}
	return -1
}

// -- Persistence --

func bookmarksFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "indigo", "plugins", "bookmarks", "bookmarks.json"), nil
}

func loadBookmarks() []bookmark {
	path, err := bookmarksFilePath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var saved []savedBookmark
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil
	}
	bmarks := make([]bookmark, len(saved))
	for i, s := range saved {
		bmarks[i] = bookmark{
			filePath: s.FilePath,
			line:     s.Line,
			col:      s.Col,
			note:     s.Note,
			active:   true,
		}
	}
	return bmarks
}

func persistBookmarks(bmarks []bookmark) {
	path, err := bookmarksFilePath()
	if err != nil {
		return
	}
	var saved []savedBookmark
	for _, b := range bmarks {
		if b.active {
			saved = append(saved, savedBookmark{
				FilePath: b.filePath,
				Line:     b.line,
				Col:      b.col,
				Note:     b.note,
			})
		}
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o755) //nolint:errcheck
	os.WriteFile(path, data, 0o644)        //nolint:errcheck
}

func main() {
	if err := sdk.Run(&Bookmarks{}); err != nil {
		fmt.Fprintf(os.Stderr, "bookmarks: %v\n", err)
		os.Exit(1)
	}
}
