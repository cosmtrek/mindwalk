package registry

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestOwnerLockSerializesProcessesAndCannotAliasRegistry(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "discovery-scan.lock")
	if OwnerLockPath(registryPath) == registryPath || DiscoveryStatePath(registryPath) == registryPath {
		t.Fatal("owner sidecars alias a custom registry path")
	}
	first, err := AcquireOwnerLock(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := AcquireOwnerLock(registryPath); !errors.Is(err, ErrOwnerStateBusy) {
		t.Fatalf("second owner mutation was not blocked: %v", err)
	}
	if held, err := OwnerLockHeld(registryPath); err != nil || !held {
		t.Fatalf("held lock = %v, %v", held, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireOwnerLock(registryPath)
	if err != nil {
		t.Fatalf("lock was not recoverable after close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}
