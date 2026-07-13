package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmtrek/mindwalk/internal/registry"
)

func initSyntheticRepository(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", path)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

func TestReposDiscoverIsPreviewOnlyThenAddsExactApprovedID(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	root := filepath.Join(home, "Projects")
	repoPath := filepath.Join(root, "approved")
	ordinaryPath := filepath.Join(root, "ordinary-directory-must-not-persist")
	initSyntheticRepository(t, repoPath)
	if err := os.MkdirAll(ordinaryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(base, "config", "repos.json")

	if err := run([]string{"repos", "discover", "-config", config, "--root", root, "--max-depth", "4", "--max-directories", "100", "--max-results", "10", "--timeout", "10"}); err != nil {
		t.Fatalf("repos discover: %v", err)
	}
	r, err := registry.Load(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.List()) != 0 {
		t.Fatalf("preview registered repositories automatically: %+v", r.List())
	}

	statePath := registry.DiscoveryStatePath(config)
	state, err := registry.LoadDiscoveryState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.LatestResults) != 1 || state.LatestResults[0].Path != repoPath {
		t.Fatalf("discovery results = %+v", state.LatestResults)
	}
	if len(state.ApprovedRoots) != 1 || state.ApprovedRoots[0] != root {
		t.Fatalf("approved roots = %v", state.ApprovedRoots)
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), ordinaryPath) {
		t.Fatal("ordinary directory scan history was persisted")
	}
	id := state.LatestResults[0].ID

	if err := run([]string{"repos", "hide-discovered", "-config", config, id}); err != nil {
		t.Fatalf("hide-discovered: %v", err)
	}
	state, _ = registry.LoadDiscoveryState(statePath)
	if !state.IsHidden(id) || !state.LatestResults[0].Hidden {
		t.Fatal("hidden discovery was not persisted")
	}
	if err := run([]string{"repos", "discovered", "-config", config, "--show-hidden"}); err != nil {
		t.Fatalf("discovered --show-hidden: %v", err)
	}
	if err := run([]string{"repos", "unhide-discovered", "-config", config, id}); err != nil {
		t.Fatalf("unhide-discovered: %v", err)
	}

	missingID := "disc_00000000000000000000000000000000"
	if err := run([]string{"repos", "add-discovered", "-config", config, id, missingID}); err == nil || !strings.Contains(err.Error(), missingID) {
		t.Fatalf("partial add-discovered result: %v", err)
	}
	r, _ = registry.Load(config)
	if got := r.List(); len(got) != 1 || got[0].Path != repoPath {
		t.Fatalf("registered repositories = %+v", got)
	}
}

func TestReposDiscoverRequiresAnExplicitRoot(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config", "repos.json")
	err := run([]string{"repos", "discover", "-config", config})
	if err == nil || !strings.Contains(err.Error(), "explicit --root") {
		t.Fatalf("discover without explicit root = %v", err)
	}
	if _, statErr := os.Stat(registry.DiscoveryStatePath(config)); !os.IsNotExist(statErr) {
		t.Fatal("discovery configuration was written without owner approval")
	}
}

func TestReposDiscoverHomeUsesOnlySyntheticHome(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "synthetic-home")
	t.Setenv("HOME", home)
	initSyntheticRepository(t, filepath.Join(home, "repo"))
	config := filepath.Join(base, "config", "repos.json")
	if err := run([]string{"repos", "discover", "-config", config, "--home", "--max-depth", "2", "--max-directories", "20", "--timeout", "10"}); err != nil {
		t.Fatalf("discover --home: %v", err)
	}
	state, err := registry.LoadDiscoveryState(registry.DiscoveryStatePath(config))
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ApprovedRoots) != 1 || state.ApprovedRoots[0] != home || len(state.LatestResults) != 1 {
		t.Fatalf("synthetic home discovery = roots %v results %+v", state.ApprovedRoots, state.LatestResults)
	}
}

func TestReposAddDiscoveredRevalidatesApprovedRoot(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	root := filepath.Join(home, "scan")
	repoPath := filepath.Join(root, "repo")
	initSyntheticRepository(t, repoPath)
	config := filepath.Join(base, "config", "repos.json")
	if err := run([]string{"repos", "discover", "-config", config, "--root", root, "--max-directories", "20", "--timeout", "10"}); err != nil {
		t.Fatal(err)
	}
	state, _ := registry.LoadDiscoveryState(registry.DiscoveryStatePath(config))
	id := state.LatestResults[0].ID
	escaped := filepath.Join(base, "escaped")
	if err := os.Rename(repoPath, escaped); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escaped, repoPath); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"repos", "add-discovered", "-config", config, id})
	if err == nil || !strings.Contains(err.Error(), "no longer validates") {
		t.Fatalf("escaped discovery add = %v", err)
	}
	r, _ := registry.Load(config)
	if len(r.List()) != 0 {
		t.Fatal("escaped repository was registered")
	}
}

func TestReposDiscoverStatusAndCrossProcessCancelMarker(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config", "repos.json")
	runtime := cliDiscoveryRuntime{
		DiscoveryProgress: registry.DiscoveryProgress{Status: "running", DirectoriesExamined: 64},
		PID:               os.Getpid(),
		StartedAt:         "2026-07-13T12:00:00Z",
		UpdatedAt:         "2026-07-13T12:00:01Z",
	}
	if err := writePrivateJSON(discoveryRuntimePath(config), runtime); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireDiscoveryLock(config)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseDiscoveryLock(lock)
	if err := run([]string{"repos", "discover-status", "-config", config}); err != nil {
		t.Fatalf("discover-status: %v", err)
	}
	if err := run([]string{"repos", "discover-cancel", "-config", config}); err != nil {
		t.Fatalf("discover-cancel: %v", err)
	}
	info, err := os.Stat(discoveryCancelPath(config))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cancel marker mode = %o", info.Mode().Perm())
	}
	runtime.Status = "completed"
	if err := writePrivateJSON(discoveryRuntimePath(config), runtime); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"repos", "discover-cancel", "-config", config}); err == nil {
		t.Fatal("completed scan accepted cancellation")
	}
}

func TestDiscoveryCancelMarkerCancelsForegroundContext(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "cancel.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go watchDiscoveryCancellation(ctx, marker, cancel, done)
	if err := writePrivateJSON(marker, map[string]string{"requestedAt": "now"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cancel marker did not cancel the foreground scan context")
	}
}

func TestDiscoverySidecarsNeverAliasCustomRegistryPath(t *testing.T) {
	for _, name := range []string{"discovery.json", "discovery-status.json", "discovery-cancel.json", "discovery-scan.lock"} {
		config := filepath.Join(t.TempDir(), name)
		paths := []string{
			config,
			registry.DiscoveryStatePath(config),
			discoveryRuntimePath(config),
			discoveryCancelPath(config),
			discoveryLockPath(config),
		}
		seen := map[string]bool{}
		for _, path := range paths {
			if seen[path] {
				t.Fatalf("custom registry sidecar collision for %s: %#v", config, paths)
			}
			seen[path] = true
		}
	}
}
