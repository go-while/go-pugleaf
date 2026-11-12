//go:build unix

package history

// Advisory lock of the reject prune (plan nntp-peering): one process per history directory
// prunes. flock is released by the kernel when the process exits, so a crash never leaves the
// prune locked out.

import (
	"errors"
	"os"
	"syscall"
)

// acquirePruneLock takes a non-blocking exclusive flock on path and returns the open file that
// holds it. ok is false without an error when another process holds the lock.
func acquirePruneLock(path string) (f *os.File, ok bool, err error) {
	f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f, true, nil
}

// releasePruneLock releases the flock and closes the file. A nil file is a no-op.
func releasePruneLock(f *os.File) {
	if f == nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		_ = err // closing the file releases the lock as well
	}
	f.Close()
}
