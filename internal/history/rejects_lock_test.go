//go:build unix

package history

// The advisory lock that makes one process the owner of the reject prune (plan nntp-peering).
// flock is per open file description, so two Open calls of one process behave like two
// processes.

import (
	"path/filepath"
	"testing"
)

func TestRejectPruneLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), pruneLockFileName)

	f1, ok, err := acquirePruneLock(path)
	if err != nil || !ok || f1 == nil {
		t.Fatalf("first acquirePruneLock = %v, %v, %v; want a held lock", f1, ok, err)
	}
	f2, ok, err := acquirePruneLock(path)
	if err != nil || ok || f2 != nil {
		releasePruneLock(f2)
		releasePruneLock(f1)
		t.Fatalf("second acquirePruneLock = %v, %v, %v; want not held, no error", f2, ok, err)
	}

	releasePruneLock(f1)
	f3, ok, err := acquirePruneLock(path)
	if err != nil || !ok || f3 == nil {
		t.Fatalf("acquirePruneLock after release = %v, %v, %v; want a held lock", f3, ok, err)
	}
	releasePruneLock(f3)
}
