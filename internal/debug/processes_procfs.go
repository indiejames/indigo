package debug

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// listProcFS reads the process table from a Linux-style /proc at root. Not
// build-tagged, so it can be tested on any platform against a directory laid
// out the same way; Linux itself is the only caller outside tests.
//
// /proc rather than running ps: a minimal container image often has no ps,
// and the container is exactly where the debugger runs.
func listProcFS(root string) ([]rawProcess, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []rawProcess
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info, err := os.Stat(dir)
		if err != nil {
			continue // exited while being read
		}
		r := rawProcess{pid: pid, uid: -1}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			r.uid = int(st.Uid) // /proc/<pid> belongs to the process's owner
		}
		if status, err := os.ReadFile(filepath.Join(dir, "status")); err == nil {
			for _, line := range strings.Split(string(status), "\n") {
				if v, ok := strings.CutPrefix(line, "PPid:"); ok {
					r.ppid, _ = strconv.Atoi(strings.TrimSpace(v))
				}
				if v, ok := strings.CutPrefix(line, "Name:"); ok {
					r.name = strings.TrimSpace(v)
				}
			}
		}
		// Unreadable for another user's process, which is left out anyway.
		if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
			r.exe = strings.TrimSuffix(exe, " (deleted)")
			r.name = filepath.Base(r.exe) // comm is cut to 15 characters
		}
		if cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
			args := strings.Split(string(bytes.TrimRight(cmdline, "\x00")), "\x00")
			if len(args) > 1 {
				r.args = args[1:]
			}
		}
		out = append(out, r)
	}
	return out, nil
}
