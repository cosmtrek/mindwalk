//go:build !windows

package main

import (
	"os"
	"syscall"

	"github.com/cosmtrek/mindwalk/internal/registry"
)

func acquireDiscoveryLock(path string) (*registry.OwnerLock, error) {
	return registry.AcquireOwnerLock(path)
}

func releaseDiscoveryLock(lock *registry.OwnerLock) {
	_ = lock.Close()
}

func discoveryLockHeld(path string) (bool, error) {
	return registry.OwnerLockHeld(path)
}

func discoveryProcessAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func discoverySignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
