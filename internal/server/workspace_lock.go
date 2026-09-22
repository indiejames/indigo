package server

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// ErrAlreadyRunning is returned by New when another live server already owns
// the workspace. A caller that raced to start one should treat it as success:
// the socket it wanted is being served, just not by this process.
var ErrAlreadyRunning = errors.New("a server is already running for this workspace")

// lockWaitTimeout bounds how long New waits for a lock whose holder has stopped
// answering on the socket — a server partway through shutting down.
const lockWaitTimeout = 2 * time.Second

// acquireWorkspaceLock takes an exclusive flock next to the socket, held for
// the life of the server.
//
// Without it, two servers could run for one workspace. New used to remove
// whatever sat at the socket path and listen, so two windows starting at the
// same instant (two panes of a layout, or two attaches to one container) each
// found no server, each started one, and the second took over the socket path
// from the first. Each window then held its own buffer table with no OT
// between them — the silent split concurrent editing exists to prevent. The
// first to exit then deleted the other's socket, so the next window started
// a third.
//
// A pre-check of "does something answer on the socket" narrows that window but
// cannot close it; a kernel lock can. flock is released by the kernel when the
// process dies, so a crashed server never leaves the workspace locked. The lock
// file itself is never deleted: unlinking a lock file lets a waiter lock the
// old inode while a newcomer locks a fresh one, which reintroduces the race.
//
// A held lock whose owner is not answering on the socket is a server partway
// through shutdown (Wait releases the lock only after unlinking the socket);
// that is waited out briefly rather than refused, so a window opened just as
// the previous one closed still gets a server.
func acquireWorkspaceLock(sockPath string) (*os.File, error) {
	lockPath := sockPath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	deadline := time.Now().Add(lockWaitTimeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close() //nolint:errcheck
			return nil, fmt.Errorf("lock %s: %w", lockPath, err)
		}
		if IsRunning(sockPath) || time.Now().After(deadline) {
			f.Close() //nolint:errcheck
			return nil, ErrAlreadyRunning
		}
		time.Sleep(20 * time.Millisecond)
	}
}
