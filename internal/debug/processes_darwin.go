package debug

import (
	"bytes"
	"encoding/binary"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// listRawProcesses reads macOS's process table through sysctl — what ps
// itself does — rather than running ps and parsing its columns, which cannot
// tell an argument containing spaces from two arguments.
func listRawProcesses() ([]rawProcess, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]rawProcess, 0, len(procs))
	for _, kp := range procs {
		r := rawProcess{
			pid:  int(kp.Proc.P_pid),
			ppid: int(kp.Eproc.Ppid),
			uid:  int(kp.Eproc.Ucred.Uid),
			name: unix.ByteSliceToString(kp.Proc.P_comm[:]), // cut to 16 characters; replaced below
		}
		if exe, args, ok := procArgs(r.pid); ok {
			r.exe, r.args = exe, args
			r.name = filepath.Base(exe)
		}
		out = append(out, r)
	}
	return out, nil
}

// procArgs reads a process's executable path and arguments from
// kern.procargs2: argc as a 32-bit integer, the executable's path, NUL
// padding, then argc NUL-terminated arguments, then the environment. Only
// readable for the user's own processes.
func procArgs(pid int) (exe string, args []string, ok bool) {
	exe, args, _, ok = procArgsEnv(pid)
	return exe, args, ok
}

// processEnv returns a process's environment, or nil when it cannot be read.
func processEnv(pid int) []string {
	_, _, env, _ := procArgsEnv(pid)
	return env
}

func procArgsEnv(pid int) (exe string, args, env []string, ok bool) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(buf) < 4 {
		return "", nil, nil, false
	}
	argc := int(binary.LittleEndian.Uint32(buf[:4]))
	rest := buf[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return "", nil, nil, false
	}
	exe = string(rest[:end])
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	for i := 0; i < argc && len(rest) > 0; i++ {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			end = len(rest)
		}
		if i > 0 { // argv[0] is the program itself
			args = append(args, string(rest[:end]))
		}
		rest = rest[min(end+1, len(rest)):]
	}
	for len(rest) > 0 {
		end := bytes.IndexByte(rest, 0)
		if end <= 0 {
			break // the environment ends at an empty string
		}
		env = append(env, string(rest[:end]))
		rest = rest[end+1:]
	}
	return exe, args, env, true
}
