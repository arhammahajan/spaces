//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package worktree

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func lockStoreFile(lockPath string, exclusive bool) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("worktree store: open lock %s: %w", lockPath, err)
	}

	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if err == nil {
			break
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		_ = f.Close()
		return nil, fmt.Errorf("worktree store: flock %s: %w", lockPath, err)
	}

	return func() {
		for {
			err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			if !errors.Is(err, syscall.EINTR) {
				break
			}
		}
		_ = f.Close()
	}, nil
}
