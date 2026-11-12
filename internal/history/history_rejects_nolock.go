//go:build !unix

package history

// Platforms without flock (plan nntp-peering): the reject prune runs without an owner lock, so
// every process prunes its own history directory. The prune is idempotent (a row is deleted
// once), so this only costs duplicated work when two processes share one directory.

import "os"

// acquirePruneLock always grants the prune to this process (no advisory lock available).
func acquirePruneLock(path string) (f *os.File, ok bool, err error) {
	return nil, true, nil
}

// releasePruneLock is a no-op without an advisory lock.
func releasePruneLock(f *os.File) {}
