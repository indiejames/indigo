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
