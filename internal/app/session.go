package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/serialq"
)

// Tab groups are saved per workspace, in ~/.indigo/groups/<hash>.json, keyed
// like the recent-files list (by the workspace's host path, so two projects
// that both mount at /workspaces/api in a container keep separate groups).
// Files are stored relative to the workspace, so they mean the same thing
// on either side of a container.
//
// A window started without a file argument restores the session: it reopens
// every saved file into its group and owns the file, writing its own groups
// over it. A window started on a file reopens nothing. It only uses the saved
// groups to place files as they open, and adds its groups to the file rather
// than replacing it, so a quick edit never shrinks the saved session. Two
// restoring windows on one workspace: the last to write wins.

// savedSession is the on-disk form of a workspace's tab groups.
type savedSession struct {
	Version int          `json:"version"`
	Active  string       `json:"active"` // the current group's name
	Groups  []savedGroup `json:"groups"`
}

// savedGroup is one group. Name "" is the ungrouped buffers.
type savedGroup struct {
	Name   string   `json:"name"`
	Files  []string `json:"files"`            // workspace-relative, in tab order
	Active string   `json:"active,omitempty"` // the group's most recently used file
}

const sessionVersion = 1

// sessionPath returns where workDir's tab groups are saved.
func sessionPath(workDir string) (string, error) {
	return workspaceStatePath("groups", recentRootOr(workDir))
}

// readSessionFile reads the session at p. ok is false when there is none, or
// it can't be read. newer is true when it was written by a newer indigo, whose
// format this one doesn't know: it is neither restored nor overwritten.
func readSessionFile(p string) (s savedSession, ok, newer bool) {
	data, err := os.ReadFile(p)
	if err != nil {
		return savedSession{}, false, false
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return savedSession{}, false, false
	}
	if s.Version > sessionVersion {
		return savedSession{}, false, true
	}
	if s.Version != sessionVersion {
		return savedSession{}, false, false
	}
	return s, true, false
}

// writeSessionFile replaces p with s through a temporary file and a rename, so
// a crash mid-write leaves the old session rather than half of a new one.
func writeSessionFile(p string, s savedSession) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".groups-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()      //nolint:errcheck
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	return nil
}

// saveSession writes s to p, merging it into what is there when merge is set.
// Under the state-file lock, so a merge's read-modify-write can't interleave
// with another window's.
func saveSession(p string, s savedSession, merge bool) {
	withStateFileLock(p, func() {
		old, ok, newer := readSessionFile(p)
		if newer {
			return
		}
		if merge && ok {
			s = mergeSession(old, s)
		}
		writeSessionFile(p, s) //nolint:errcheck
	})
}

// mergeSession adds contrib's groups to base: each of contrib's files moves
// to its group in contrib, which is created at the end when base has none.
// A file already in that group keeps its place, and nothing else in base
// changes, so files contrib doesn't mention stay where they were.
func mergeSession(base, contrib savedSession) savedSession {
	target := make(map[string]string) // file → its group in contrib
	for _, g := range contrib.Groups {
		for _, f := range g.Files {
			target[f] = g.Name
		}
	}
	movedFrom := func(group, f string) bool {
		name, ok := target[f]
		return ok && name != group
	}
	out := savedSession{Version: sessionVersion, Active: base.Active}
	for _, g := range base.Groups {
		files := slices.DeleteFunc(slices.Clone(g.Files), func(f string) bool { return movedFrom(g.Name, f) })
		active := g.Active
		if movedFrom(g.Name, active) {
			active = ""
		}
		out.Groups = append(out.Groups, savedGroup{Name: g.Name, Files: files, Active: active})
	}
	for _, g := range contrib.Groups {
		gi := slices.IndexFunc(out.Groups, func(o savedGroup) bool { return o.Name == g.Name })
		if gi < 0 {
			out.Groups = append(out.Groups, savedGroup{Name: g.Name})
			gi = len(out.Groups) - 1
		}
		for _, f := range g.Files {
			if !slices.Contains(out.Groups[gi].Files, f) {
				out.Groups[gi].Files = append(out.Groups[gi].Files, f)
			}
		}
		if out.Groups[gi].Active == "" {
			out.Groups[gi].Active = g.Active
		}
	}
	out.Groups = slices.DeleteFunc(out.Groups, func(g savedGroup) bool { return len(g.Files) == 0 })
	if !slices.ContainsFunc(out.Groups, func(g savedGroup) bool { return g.Name == out.Active }) {
		out.Active = contrib.Active
	}
	return out
}

// sessionWrites runs session writes one at a time, in the order they were
// made, off the UI thread. Push never blocks.
var sessionWrites serialq.Queue

// FlushSessionWrites waits, for at most timeout, for the session writes
// already made to reach disk. main calls it once the window has closed,
// since the process would otherwise exit with the last write still queued.
func FlushSessionWrites(timeout time.Duration) {
	done := make(chan struct{})
	sessionWrites.Push(func() { close(done) })
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// RestoreSession makes this window restore the workspace's saved tab groups
// at startup and own the saved session. main calls it for a window started
// without a file argument.
func (a *App) RestoreSession() { a.restoreSession = true }

// saveSessionIfChanged queues a write of the groups when they, a file's path
// or the active tab have changed since the last one. Run once per Update.
//
// Nothing is written before the saved session has been read (and, in a
// restoring window, reopened), or the window's opening state would replace
// it. Nor with no buffers left: closing the last one quits, and the saved
// session is then the one from just before.
func (a App) saveSessionIfChanged() App {
	if !a.sessionReady || len(a.buffers) == 0 {
		return a
	}
	mark := a.sessionFingerprint()
	if mark == a.sessionMark {
		return a
	}
	a.sessionMark = mark
	p, err := sessionPath(a.workDir)
	if err != nil {
		return a
	}
	s, merge := a.sessionSnapshot(), !a.restoreSession
	sessionWrites.Push(func() { saveSession(p, s, merge) })
	return a
}

// sessionFingerprint changes whenever what sessionSnapshot would save does.
func (a App) sessionFingerprint() string {
	paths := make(map[uint32]string, len(a.buffers))
	for _, m := range a.buffers {
		paths[m.BufID()] = m.FilePath()
	}
	var b strings.Builder
	b.WriteString(a.groupName)
	if a.active >= 0 && a.active < len(a.buffers) {
		b.WriteString("\x00" + a.buffers[a.active].FilePath())
	}
	for _, g := range a.groups {
		b.WriteString("\x01" + g.name)
		for _, id := range g.bufIDs {
			b.WriteString("\x02" + paths[id])
		}
	}
	return b.String()
}

// sessionRel returns abs relative to the workspace, or false for an untitled
// buffer or a file outside it, which are not saved.
func (a App) sessionRel(abs string) (string, bool) {
	if abs == "" {
		return "", false
	}
	rel, err := filepath.Rel(a.workDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// sessionSnapshot is the window's groups as they are saved.
func (a App) sessionSnapshot() savedSession {
	s := savedSession{Version: sessionVersion, Active: a.groupName}
	for _, g := range a.groups {
		sg := savedGroup{Name: g.name}
		for _, id := range g.bufIDs {
			if i := a.bufIndex(id); i >= 0 {
				if rel, ok := a.sessionRel(a.buffers[i].FilePath()); ok {
					sg.Files = append(sg.Files, rel)
				}
			}
		}
		if i := a.mostRecentIn(g.bufIDs); i >= 0 {
			if rel, ok := a.sessionRel(a.buffers[i].FilePath()); ok {
				sg.Active = rel
			}
		}
		if len(sg.Files) > 0 {
			s.Groups = append(s.Groups, sg)
		}
	}
	return s
}

// sessionLoadedMsg carries the saved session read at startup.
type sessionLoadedMsg struct {
	s  savedSession
	ok bool
}

// loadSession reads the workspace's saved session. Every window does, to
// place files in their saved groups as they open.
func (a App) loadSession() tea.Cmd {
	workDir := a.workDir
	return func() tea.Msg {
		p, err := sessionPath(workDir)
		if err != nil {
			return sessionLoadedMsg{}
		}
		s, ok, _ := readSessionFile(p)
		return sessionLoadedMsg{s: s, ok: ok}
	}
}

// handleSessionLoaded records where the saved session puts each file, moves
// the buffers already open into their saved groups, and in a restoring window
// reopens the rest.
func (a App) handleSessionLoaded(msg sessionLoadedMsg) (tea.Model, tea.Cmd) {
	if a.placement == nil {
		a.placement = make(map[string]string)
	}
	var files, groupActive []string
	var active string
	if msg.ok {
		// The saved group wins over the one a file joined before the session
		// was read: that was only the default for a file with no group.
		listed := make(map[string]bool)
		for _, g := range msg.s.Groups {
			for _, rel := range g.Files {
				abs := filepath.Join(a.workDir, rel)
				if listed[abs] {
					continue // a hand-edited file listing one file twice: the first wins
				}
				listed[abs] = true
				a.placement[abs] = g.Name
				files = append(files, abs)
			}
			if g.Active != "" {
				abs := filepath.Join(a.workDir, g.Active)
				groupActive = append(groupActive, abs)
				if g.Name == msg.s.Active {
					active = abs
				}
			}
		}
	}
	for _, m := range a.buffers {
		if name, ok := a.placement[m.FilePath()]; ok && m.FilePath() != "" {
			a = a.placeBuffer(m.BufID(), name)
		}
	}
	files = slices.DeleteFunc(files, func(abs string) bool {
		return slices.ContainsFunc(a.buffers, func(m client.Model) bool { return m.FilePath() == abs })
	})
	if !a.restoreSession || len(files) == 0 {
		a.sessionReady = true
		return a, a.resizeAllBuffers() // a saved group can bring in the strip
	}
	return a, a.restoreFiles(files, active, groupActive)
}

// sessionRestoredMsg carries the buffers a restoring window reopened.
type sessionRestoredMsg struct {
	models      []client.Model
	missing     int      // saved files that no longer exist or failed to open
	active      string   // the saved active tab
	groupActive []string // each group's saved active tab
}

// restoreFiles reopens files, in order, through the server. Each is checked
// with StatPath first, because OpenFile makes an empty buffer for a path that
// doesn't exist, which would bring a deleted file back as an empty one. The
// server is asked rather than the local filesystem since, attached to a
// container, this process may not see the workspace.
func (a App) restoreFiles(files []string, active string, groupActive []string) tea.Cmd {
	rpc, cfg, workDir := a.rpc, a.cfg, a.workDir
	return func() tea.Msg {
		out := sessionRestoredMsg{active: active, groupActive: groupActive}
		for _, abs := range files {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			exists, isDir, err := rpc.StatPath(ctx, abs)
			if err != nil || !exists || isDir {
				cancel()
				out.missing++
				continue
			}
			bufID, content, version, fromRecovery, generation, err := rpc.OpenFile(ctx, abs)
			cancel()
			if err != nil {
				out.missing++
				continue
			}
			out.models = append(out.models, client.New(rpc, bufID, content, version, abs, workDir, cfg, fromRecovery, generation))
		}
		return out
	}
}

// handleSessionRestored adds the reopened buffers and returns to the saved
// tab.
func (a App) handleSessionRestored(msg sessionRestoredMsg) (tea.Model, tea.Cmd) {
	a.sessionReady = true
	if msg.missing > 0 {
		a.status = fmt.Sprintf("%d file(s) in the saved tab groups no longer exist", msg.missing)
	}
	if len(msg.models) == 0 {
		return a, nil
	}
	hadBuffers := len(a.buffers) > 0
	var cmds []tea.Cmd
	prev := uint32(0)
	if a.active >= 0 && a.active < len(a.buffers) {
		prev = a.buffers[a.active].BufID()
	}
	// The untitled buffer a no-argument start opens is dropped once the
	// session brings back real files, unless it has been typed in.
	if i := slices.IndexFunc(a.buffers, func(m client.Model) bool { return m.FilePath() == "" && !m.Dirty() }); i >= 0 {
		id := a.buffers[i].BufID()
		a.forgetTabUse(id)
		a.buffers = slices.Delete(slices.Clone(a.buffers), i, i+1)
		rpc := a.rpc
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			rpc.CloseBuffer(ctx, id) //nolint:errcheck
			return nil
		})
		a.active = 0
		a = a.syncGroups()
	}
	for _, m := range msg.models {
		if a.bufIndex(m.BufID()) >= 0 {
			continue // opened by hand while the session was being restored
		}
		a.buffers = append(a.buffers, m)
		cmds = append(cmds, m.Init())
	}
	// Stamp each group's saved tab as its most recent, so switching to a
	// group lands where it was left.
	if a.tabUse == nil {
		a.tabUse = make(map[uint32]uint64, len(a.buffers))
	}
	for _, abs := range msg.groupActive {
		if i := slices.IndexFunc(a.buffers, func(m client.Model) bool { return m.FilePath() == abs }); i >= 0 {
			a.tabSeq++
			a.tabUse[a.buffers[i].BufID()] = a.tabSeq
		}
	}
	switch {
	case slices.ContainsFunc(a.buffers, func(m client.Model) bool { return m.FilePath() == msg.active }):
		a.active = slices.IndexFunc(a.buffers, func(m client.Model) bool { return m.FilePath() == msg.active })
	case a.bufIndex(prev) >= 0:
		a.active = a.bufIndex(prev)
	default:
		a.active = 0
	}
	a = a.syncGroups()
	// The picker a directory start opens with stands in for having nothing
	// open; now something is. One already being typed in stays.
	if !hadBuffers && a.picker != nil && a.picker.query == "" {
		a.picker = nil
	}
	cmds = append(cmds, a.resizeAllBuffers(), a.buffers[a.active].ReportActiveContextCmd())
	return a, tea.Batch(cmds...)
}
