// Package lock provides an advisory, process-wide file lock so concurrent
// dial invocations (menu bar, CLI, remote sync) never interleave a switch.
package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Lock struct{ f *os.File }

// Acquire blocks until the lock at path is held or timeout elapses.
func Acquire(path string, timeout time.Duration) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another dial process holds %s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
}
