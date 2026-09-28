package debug

import (
	"debug/buildinfo"
	"os"
	"sort"
	"sync"
	"time"
)

// Process is a process the user could attach the debugger to.
type Process struct {
	PID, PPID int
	Name      string   // the executable's base name
	Exe       string   // the executable's path, when it could be read
	Args      []string // the arguments after argv[0]
	// GoVersion and GoModule are set for a Go program, read from the build
	// information Go embeds in every binary it links: the toolchain, and the
	// main module ("github.com/you/server"), which names a program far better
	// than its executable does.
	GoVersion string
	GoModule  string
}

// IsGo reports whether p is a Go program.
func (p Process) IsGo() bool { return p.GoVersion != "" }

// rawProcess is what a platform's process table gives, before filtering.
type rawProcess struct {
	pid, ppid, uid int
	name, exe      string
	args           []string
}

// ListProcesses returns the processes the current user could attach to,
// Go programs first. It leaves out other users' processes, which a debugger
// could not attach to anyway, and this process with everything it started —
// the editor's own plugins, language servers and debug adapters, and a
// program already being debugged.
//
// It runs in the server, so inside a container it lists the container's
// processes: the machine the debugger runs on.
func ListProcesses() ([]Process, error) {
	raw, err := listRawProcesses()
	if err != nil {
		return nil, err
	}
	return filterProcesses(raw, os.Getuid(), os.Getpid()), nil
}

func filterProcesses(raw []rawProcess, uid, self int) []Process {
	parent := make(map[int]int, len(raw))
	for _, r := range raw {
		parent[r.pid] = r.ppid
	}
	ours := func(pid int) bool { // pid is self or started, directly or not, by self
		for seen := 0; pid > 1 && seen < 64; seen++ {
			if pid == self {
				return true
			}
			pid = parent[pid]
		}
		return false
	}
	var out []Process
	for _, r := range raw {
		if r.uid != uid || r.pid <= 1 || ours(r.pid) {
			continue
		}
		p := Process{PID: r.pid, PPID: r.ppid, Name: r.name, Exe: r.exe, Args: r.args}
		if r.exe != "" {
			p.GoVersion, p.GoModule = goBuildInfo(r.exe)
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsGo() != out[j].IsGo() {
			return out[i].IsGo()
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].PID < out[j].PID
	})
	return out
}

// goBuildCache remembers what each executable is, keyed by path and
// modification time: a machine runs many copies of a few executables (every
// browser helper, every shell), and the picker may be opened often.
var goBuildCache = struct {
	sync.Mutex
	m map[string]goBuild
}{m: map[string]goBuild{}}

type goBuild struct {
	mtime           time.Time
	version, module string
}

// goBuildInfo returns the Go toolchain version and main module of the
// executable at path, or "" for anything that is not a Go program.
func goBuildInfo(path string) (version, module string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", ""
	}
	goBuildCache.Lock()
	c, ok := goBuildCache.m[path]
	goBuildCache.Unlock()
	if ok && c.mtime.Equal(info.ModTime()) {
		return c.version, c.module
	}
	c = goBuild{mtime: info.ModTime()}
	if bi, err := buildinfo.ReadFile(path); err == nil {
		c.version = bi.GoVersion
		c.module = bi.Main.Path
		if c.module == "" {
			c.module = bi.Path // a program built outside any module
		}
	}
	goBuildCache.Lock()
	goBuildCache.m[path] = c
	goBuildCache.Unlock()
	return c.version, c.module
}
