package debug

import (
	"bytes"
	"os"
	"strconv"
)

func listRawProcesses() ([]rawProcess, error) { return listProcFS("/proc") }

// processEnv returns a process's environment, or nil when it cannot be read.
func processEnv(pid int) []string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return nil
	}
	var env []string
	for _, kv := range bytes.Split(bytes.TrimRight(data, "\x00"), []byte{0}) {
		if len(kv) > 0 {
			env = append(env, string(kv))
		}
	}
	return env
}

// processStopped reports whether pid is stopped (T, or t for traced), and
// false when it cannot tell. The state is the field after the command, which
// is parenthesised and may itself contain spaces or parentheses.
func processStopped(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+2 >= len(data) {
		return false
	}
	return data[i+2] == 'T' || data[i+2] == 't'
}
