//go:build windows

package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

var ErrOwnerStateBusy = errors.New("repository owner state is busy")

type OwnerLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func OwnerLockPath(registryPath string) string { return registryPath + ".owner.lock" }

func AcquireOwnerLock(registryPath string) (*OwnerLock, error) {
	path := OwnerLockPath(registryPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	lock := &OwnerLock{file: f}
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &lock.overlapped)
	if err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, ErrOwnerStateBusy
		}
		return nil, fmt.Errorf("lock repository owner state: %w", err)
	}
	return lock, nil
}

func (l *OwnerLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	unlockErr := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &l.overlapped)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func OwnerLockHeld(registryPath string) (bool, error) {
	lock, err := AcquireOwnerLock(registryPath)
	if errors.Is(err, ErrOwnerStateBusy) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, lock.Close()
}
