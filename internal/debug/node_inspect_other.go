//go:build !unix

package debug

import "errors"

func signalInspector(int) error {
	return errors.New("switching on a Node inspector by process id is not supported here")
}

func continueProcess(int) error { return errors.New("not supported here") }
