package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cosmtrek/mindwalk/internal/registry"
)

// cliDiscoveryRuntime is deliberately separate from durable discovery state.
// It contains only bounded aggregate progress and process-control metadata; it
// never records the ordinary directories examined by the scanner.
type cliDiscoveryRuntime struct {
	registry.DiscoveryProgress
	PID         int    `json:"pid,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	LimitReason string `json:"limitReason,omitempty"`
}

type repeatedString []string

func (r *repeatedString) String() string { return strings.Join(*r, ",") }
func (r *repeatedString) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value must not be empty")
	}
	*r = append(*r, value)
	return nil
}

func reposDiscover(args []string) error {
	fs, config := reposFlags("discover")
	var roots, exclusions repeatedString
	fs.Var(&roots, "root", "owner-approved scan root (repeatable)")
	fs.Var(&exclusions, "exclude", "custom directory basename or absolute path to exclude (repeatable)")
	home := fs.Bool("home", false, "explicitly approve and scan the current user's home directory")
	defaults := registry.DefaultDiscoveryOptions()
	maxDepth := fs.Int("max-depth", defaults.MaxDepth, "maximum directory depth")
	maxDirectories := fs.Int("max-directories", defaults.MaxDirectories, "maximum directories examined")
	maxResults := fs.Int("max-results", defaults.MaxResults, "maximum repositories returned")
	timeout := fs.Int("timeout", defaults.TimeoutSeconds, "scan timeout in seconds")
	findNested := fs.Bool("find-nested", false, "continue inside repositories to find nested repositories")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: mindwalk repos discover [--root PATH]... [--home]")
	}
	if *home {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home directory: %w", err)
		}
		roots = append(roots, homeDir)
	}
	if len(roots) == 0 {
		return fmt.Errorf("discovery is disabled until an explicit --root PATH or --home is provided")
	}
	if strings.TrimSpace(*config) == "" {
		return fmt.Errorf("repository registry path is unavailable")
	}
	runtimePath := discoveryRuntimePath(*config)
	cancelPath := discoveryCancelPath(*config)
	scanLock, err := acquireDiscoveryLock(*config)
	if err != nil {
		return err
	}
	defer releaseDiscoveryLock(scanLock)

	statePath := registry.DiscoveryStatePath(*config)
	state, err := registry.LoadDiscoveryState(statePath)
	if err != nil {
		return err
	}
	protected := discoveryProtectedPaths(*config)
	if err := state.SetApprovedRoots(roots, protected...); err != nil {
		return err
	}
	if len(exclusions) > 0 {
		if err := state.SetCustomExclusions(exclusions); err != nil {
			return err
		}
	}
	options := registry.DiscoveryOptions{
		MaxDepth:       *maxDepth,
		MaxDirectories: *maxDirectories,
		MaxResults:     *maxResults,
		TimeoutSeconds: *timeout,
		FindNested:     *findNested,
	}
	if err := state.SetOptions(options); err != nil {
		return err
	}
	// Persist only the explicitly approved configuration before starting. A
	// crash cannot turn a preview into a registration or lose owner choices.
	if err := state.Save(statePath); err != nil {
		return err
	}
	r, err := registry.Load(*config)
	if err != nil {
		return err
	}

	if err := removeIfExists(cancelPath); err != nil {
		return err
	}
	started := time.Now().UTC().Format(time.RFC3339)
	runtime := cliDiscoveryRuntime{
		DiscoveryProgress: registry.DiscoveryProgress{Status: "running"},
		PID:               os.Getpid(),
		StartedAt:         started,
		UpdatedAt:         started,
	}
	if err := writePrivateJSON(runtimePath, runtime); err != nil {
		return err
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), discoverySignals()...)
	defer stopSignals()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchDone := make(chan struct{})
	go watchDiscoveryCancellation(ctx, cancelPath, cancel, watchDone)
	defer close(watchDone)
	defer removeIfExists(cancelPath) // best-effort cleanup; the scan result remains durable

	var progressWriteErr error
	outcome, scanErr := (registry.DiscoveryScanner{}).Scan(ctx, registry.DiscoveryScanRequest{
		Roots:            state.ApprovedRoots,
		CustomExclusions: state.CustomExclusions,
		ProtectedPaths:   protected,
		Options:          state.Options,
		Registered:       r.List(),
		HiddenTokens:     state.HiddenTokens,
		OnProgress: func(progress registry.DiscoveryProgress) {
			runtime.DiscoveryProgress = progress
			runtime.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			if err := writePrivateJSON(runtimePath, runtime); err != nil && progressWriteErr == nil {
				progressWriteErr = err
				cancel()
			}
		},
	})
	if progressWriteErr != nil {
		return fmt.Errorf("write discovery progress: %w", progressWriteErr)
	}

	// Cancellation and timeout are normal bounded terminal states. Preserve
	// their partial repository-only results and report the state truthfully.
	if scanErr != nil && !errors.Is(scanErr, context.Canceled) && !errors.Is(scanErr, context.DeadlineExceeded) {
		runtime.Status = "failed"
		runtime.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writePrivateJSON(runtimePath, runtime)
		return scanErr
	}
	latestState, err := registry.LoadDiscoveryState(statePath)
	if err != nil {
		return err
	}
	latestState.MergeLatestForRoots(outcome, outcome.ScannedRoots)
	if err := latestState.Save(statePath); err != nil {
		return err
	}
	runtime.DiscoveryProgress = outcome.Summary.DiscoveryProgress
	runtime.StartedAt = outcome.Summary.StartedAt
	runtime.FinishedAt = outcome.Summary.FinishedAt
	runtime.LimitReason = outcome.Summary.LimitReason
	runtime.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writePrivateJSON(runtimePath, runtime); err != nil {
		return err
	}

	if err := writeJSON("", outcome); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "preview only: no repositories were added; approve IDs with mindwalk repos add-discovered <id>...")
	return nil
}

func reposDiscoverStatus(args []string) error {
	fs, config := reposFlags("discover-status")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: mindwalk repos discover-status")
	}
	var runtime cliDiscoveryRuntime
	if err := readJSONFile(discoveryRuntimePath(*config), &runtime); err == nil {
		if runtime.Status == "running" {
			held, lockErr := discoveryLockHeld(*config)
			if lockErr != nil {
				return lockErr
			}
			if !held {
				runtime.Status = "interrupted"
				runtime.PID = 0
				runtime.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				if writeErr := writePrivateJSON(discoveryRuntimePath(*config), runtime); writeErr != nil {
					return writeErr
				}
			}
		}
		return writeJSON("", runtime)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state, err := registry.LoadDiscoveryState(registry.DiscoveryStatePath(*config))
	if err != nil {
		return err
	}
	if state.LastSummary != nil {
		return writeJSON("", state.LastSummary)
	}
	return writeJSON("", cliDiscoveryRuntime{DiscoveryProgress: registry.DiscoveryProgress{Status: "idle"}})
}

func reposDiscoverCancel(args []string) error {
	fs, config := reposFlags("discover-cancel")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: mindwalk repos discover-cancel")
	}
	var runtime cliDiscoveryRuntime
	if err := readJSONFile(discoveryRuntimePath(*config), &runtime); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no discovery scan is running")
		}
		return err
	}
	if runtime.Status != "running" {
		return fmt.Errorf("no discovery scan is running (last status: %s)", runtime.Status)
	}
	held, err := discoveryLockHeld(*config)
	if err != nil {
		return err
	}
	if !held {
		return fmt.Errorf("no live discovery process is running")
	}
	if !discoveryProcessAlive(runtime.PID) {
		return fmt.Errorf("no live discovery process is running")
	}
	if err := writePrivateJSON(discoveryCancelPath(*config), map[string]string{
		"requestedAt": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	fmt.Println("discovery cancellation requested")
	return nil
}

func reposDiscovered(args []string) error {
	fs, config := reposFlags("discovered")
	showHidden := fs.Bool("show-hidden", false, "include recoverable hidden discoveries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: mindwalk repos discovered [--show-hidden]")
	}
	state, err := registry.LoadDiscoveryState(registry.DiscoveryStatePath(*config))
	if err != nil {
		return err
	}
	r, err := registry.Load(*config)
	if err != nil {
		return err
	}
	refreshDiscoveryRegistration(state, r.List())
	results := make([]registry.DiscoveryResult, 0, len(state.LatestResults))
	for _, result := range state.LatestResults {
		if !result.Hidden || *showHidden {
			results = append(results, result)
		}
	}
	return writeJSON("", results)
}

func reposAddDiscovered(args []string) error {
	fs, config := reposFlags("add-discovered")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: mindwalk repos add-discovered <id>...")
	}
	ownerLock, err := registry.AcquireOwnerLock(*config)
	if err != nil {
		return err
	}
	defer ownerLock.Close()
	statePath := registry.DiscoveryStatePath(*config)
	state, err := registry.LoadDiscoveryState(statePath)
	if err != nil {
		return err
	}
	r, err := registry.Load(*config)
	if err != nil {
		return err
	}
	byID := discoveryResultsByID(state.LatestResults)
	seen := map[string]bool{}
	var failures []string
	for _, id := range fs.Args() {
		if seen[id] {
			continue
		}
		seen[id] = true
		result, ok := byID[id]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: not in the latest discovery results", id))
			continue
		}
		if result.Hidden || !result.Accessible || result.Type == registry.DiscoveryTypeBroken {
			failures = append(failures, fmt.Sprintf("%s: repository is hidden, inaccessible, or has broken Git metadata", id))
			continue
		}
		if !withinAnyApprovedRoot(state.ApprovedRoots, result.Path) {
			failures = append(failures, fmt.Sprintf("%s: repository no longer validates inside an approved scan root", id))
			continue
		}
		if validationErr := registry.ValidateDiscoveryCandidate(result.Path, result.Type, state.ApprovedRoots); validationErr != nil {
			failures = append(failures, fmt.Sprintf("%s: Git metadata changed or no longer validates inside approved roots", id))
			continue
		}
		canonical := result.Path
		if canonicalErr := registry.ValidateCanonicalRepoPath(canonical); canonicalErr != nil || !withinAnyApprovedRoot(state.ApprovedRoots, canonical) {
			failures = append(failures, fmt.Sprintf("%s: repository no longer validates inside an approved scan root", id))
			continue
		}
		repo, addErr := r.AddValidatedDiscovery(canonical, result.Name, result.Type, state.ApprovedRoots)
		if addErr == nil && (repo.Path != canonical || !withinAnyApprovedRoot(state.ApprovedRoots, repo.Path)) {
			_ = r.Remove(repo.ID)
			addErr = fmt.Errorf("repository path changed during final registration")
		}
		if addErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", id, addErr))
			continue
		}
		// Save after each successful item. A later validation failure never rolls
		// back repositories the owner already approved successfully.
		if err := r.Save(); err != nil {
			return fmt.Errorf("save repository %s: %w", id, err)
		}
		fmt.Printf("added %s  %s  %s\n", repo.ID, repo.Name, repo.Path)
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("some discovered repositories were not added: %s", strings.Join(failures, "; "))
	}
	return nil
}

func reposSetDiscoveryHidden(args []string, hidden bool) error {
	verb := "hide-discovered"
	if !hidden {
		verb = "unhide-discovered"
	}
	fs, config := reposFlags(verb)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: mindwalk repos %s <id>...", verb)
	}
	ownerLock, err := registry.AcquireOwnerLock(*config)
	if err != nil {
		return err
	}
	defer ownerLock.Close()
	statePath := registry.DiscoveryStatePath(*config)
	state, err := registry.LoadDiscoveryState(statePath)
	if err != nil {
		return err
	}
	known := discoveryResultsByID(state.LatestResults)
	seen := map[string]bool{}
	for _, id := range fs.Args() {
		if seen[id] {
			continue
		}
		seen[id] = true
		if hidden {
			if _, ok := known[id]; !ok {
				return fmt.Errorf("%s is not in the latest discovery results", id)
			}
		} else if !state.IsHidden(id) {
			return fmt.Errorf("%s is not hidden", id)
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	if hidden {
		err = state.Hide(ids...)
	} else {
		err = state.Unhide(ids...)
	}
	if err != nil {
		return err
	}
	if err := state.Save(statePath); err != nil {
		return err
	}
	fmt.Printf("%s %d discovered repository(s)\n", map[bool]string{true: "hidden", false: "unhidden"}[hidden], len(ids))
	return nil
}

func discoveryProtectedPaths(config string) []string {
	return []string{
		filepath.Dir(config),
		config,
		registry.DiscoveryStatePath(config),
		discoveryRuntimePath(config),
		discoveryCancelPath(config),
		discoveryLockPath(config),
	}
}

func discoveryRuntimePath(config string) string {
	return config + ".discovery-status.json"
}

func discoveryCancelPath(config string) string {
	return config + ".discovery-cancel.json"
}

func discoveryLockPath(config string) string {
	return registry.OwnerLockPath(config)
}

func watchDiscoveryCancellation(ctx context.Context, marker string, cancel context.CancelFunc, done <-chan struct{}) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if _, err := os.Lstat(marker); err == nil {
				cancel()
				return
			}
		}
	}
}

func refreshDiscoveryRegistration(state *registry.DiscoveryState, repos []registry.Repo) {
	registered := make(map[string]bool, len(repos))
	for _, repo := range repos {
		registered[repo.Path] = true
	}
	for i := range state.LatestResults {
		state.LatestResults[i].AlreadyRegistered = registered[state.LatestResults[i].Path]
	}
}

func discoveryResultsByID(results []registry.DiscoveryResult) map[string]registry.DiscoveryResult {
	byID := make(map[string]registry.DiscoveryResult, len(results))
	for _, result := range results {
		byID[result.ID] = result
	}
	return byID
}

func withinAnyApprovedRoot(roots []string, path string) bool {
	for _, root := range roots {
		if registry.WithinCanonical(root, path) {
			return true
		}
	}
	return false
}

func writePrivateJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".discovery-cli-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readJSONFile(path string, value any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, value); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
