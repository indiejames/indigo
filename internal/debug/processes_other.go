//go:build !linux && !darwin

package debug

import (
	"errors"
	"runtime"
)

func listRawProcesses() ([]rawProcess, error) {
	return nil, errors.New("listing processes is not supported on " + runtime.GOOS)
}

func processEnv(int) []string { return nil }

func processStopped(int) bool { return false }
