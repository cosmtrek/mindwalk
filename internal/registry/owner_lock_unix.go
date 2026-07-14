//go:build !windows

package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrOwnerStateBusy means another CLI or server process owns the registry
// mutation transaction. Callers must fail closed instead of overwriting a
// snapshot loaded before that process saved.
var ErrOwnerStateBusy = errors.New("repository owner state is busy")

type OwnerLock struct {
	file *os.File
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
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrOwnerStateBusy
		}
		return nil, fmt.Errorf("lock repository owner state: %w", err)
	}
	return &OwnerLock{file: f}, nil
}

func (l *OwnerLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
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
