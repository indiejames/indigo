package client

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/indiejames/indigo/internal/document"
)

// clipboardWriter is the seam production code uses to write the system
// clipboard; tests override it (see TestMain) so running the suite never
// mutates the developer's real clipboard.
var clipboardWriter = writeClipboard

// readClipboard returns the current system clipboard contents.
func readClipboard() (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbpaste")
	case "linux":
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard", "-o")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--output")
		} else if _, err := exec.LookPath("wl-paste"); err == nil {
			cmd = exec.Command("wl-paste", "--no-newline")
		} else {
			return "", fmt.Errorf("no clipboard tool found (install xclip, xsel, or wl-copy)")
		}
	default:
		return "", fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}
	out, err := cmd.Output()
	// Text copied on Windows, or from a CRLF file in another editor, carries
	// "\r\n". The buffer holds "\n" only — see document.NormalizeNewlines.
	return document.NormalizeNewlines(string(out)), err
}

// writeClipboard copies text to the system clipboard.
func writeClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		} else if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd = exec.Command("wl-copy")
		} else {
			return fmt.Errorf("no clipboard tool found (install xclip, xsel, or wl-copy)")
		}
	default:
		return fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
