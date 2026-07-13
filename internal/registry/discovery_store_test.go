package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoveryStateRoundTripPermissionsAndRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "Code")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config", "discovery.json")
	state := NewDiscoveryState()
	if err := state.SetApprovedRoots([]string{root, root}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetCustomExclusions([]string{"generated", "generated", filepath.Join(root, "scratch")}); err != nil {
		t.Fatal(err)
	}
	options := DefaultDiscoveryOptions()
	options.MaxDepth = 7
	options.FindNested = true
	if err := state.SetOptions(options); err != nil {
		t.Fatal(err)
	}
	result := DiscoveryResult{ID: discoveryID(root), Name: "Code", Path: root, Type: DiscoveryTypeRepository, State: DiscoveryStateUnknown, DiscoveryRoot: root, Accessible: true}
	outcome := DiscoveryOutcome{Results: []DiscoveryResult{result}, Summary: DiscoverySummary{DiscoveryProgress: DiscoveryProgress{Status: "completed", DirectoriesExamined: 3, RepositoriesFound: 1}, FinishedAt: "2026-07-13T12:00:00Z"}}
	state.SetLatest(outcome)
	if err := state.Hide(result.ID); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode: info=%v err=%v", info, err)
	}

	loaded, err := LoadDiscoveryState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.ApprovedRoots) != 1 || loaded.Options.MaxDepth != 7 || !loaded.Options.FindNested {
		t.Fatalf("preferences lost: %+v", loaded)
	}
	if len(loaded.LatestResults) != 1 || !loaded.LatestResults[0].Hidden || !loaded.IsHidden(result.ID) {
		t.Fatalf("results/hidden lost: %+v", loaded)
	}
	if loaded.LastSummary == nil || loaded.LastSummary.DirectoriesExamined != 3 {
		t.Fatalf("summary lost: %+v", loaded.LastSummary)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "directoriesExaminedPaths") || strings.Contains(string(b), "ordinaryDirectories") {
		t.Fatalf("ordinary directory history persisted: %s", b)
	}
}

func TestDiscoveryStateMissingUsesDefaults(t *testing.T) {
	state, err := LoadDiscoveryState(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != DiscoverySchemaVersion || state.Options != DefaultDiscoveryOptions() {
		t.Fatalf("defaults = %+v", state)
	}
}

func TestDiscoveryStateHideUnhideAndForget(t *testing.T) {
	root := t.TempDir()
	id := discoveryID(root)
	state := NewDiscoveryState()
	state.LatestResults = []DiscoveryResult{{ID: id, Path: root, Name: "repo", Type: DiscoveryTypeRepository, State: DiscoveryStateUnknown, DiscoveryRoot: root}}
	state.LastSummary = &DiscoverySummary{DiscoveryProgress: DiscoveryProgress{Status: "completed"}}
	state.LastScanTime = "2026-07-13T12:00:00Z"
	if err := state.Hide(id); err != nil {
		t.Fatal(err)
	}
	if !state.LatestResults[0].Hidden {
		t.Fatal("latest result not hidden")
	}
	if err := state.Unhide(id); err != nil {
		t.Fatal(err)
	}
	if state.LatestResults[0].Hidden || state.IsHidden(id) {
		t.Fatal("result remained hidden")
	}
	if err := state.Hide("bad"); err == nil {
		t.Fatal("invalid hidden token accepted")
	}
	state.ForgetScanHistory()
	if state.LatestResults != nil || state.LastSummary != nil || state.LastScanTime != "" {
		t.Fatalf("history not forgotten: %+v", state)
	}
}

func TestDiscoveryStateRejectsInvalidConfigurationAndTampering(t *testing.T) {
	state := NewDiscoveryState()
	invalidOptions := DefaultDiscoveryOptions()
	invalidOptions.MaxDepth = 1000
	if err := state.SetOptions(invalidOptions); err == nil {
		t.Fatal("invalid options accepted")
	}
	if err := state.SetCustomExclusions([]string{"nested/path"}); err == nil {
		t.Fatal("unsafe custom exclusion accepted")
	}

	path := filepath.Join(t.TempDir(), "discovery.json")
	tampered := map[string]any{
		"schemaVersion": DiscoverySchemaVersion,
		"options":       DefaultDiscoveryOptions(),
		"approvedRoots": []string{}, "customExclusions": []string{}, "hiddenTokens": []string{},
		"latestResults": []map[string]any{{"id": "disc_00000000000000000000000000000000", "name": "bad", "path": t.TempDir(), "type": "repository", "state": "UNKNOWN", "discoveryRoot": t.TempDir()}},
	}
	b, _ := json.Marshal(tampered)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDiscoveryState(path); err == nil {
		t.Fatal("tampered result id accepted")
	}

	if err := os.WriteFile(path, []byte(`{"schemaVersion":99,"options":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDiscoveryState(path); err == nil {
		t.Fatal("unknown schema accepted")
	}
}

func TestDiscoveryStatePathIsBesideRegistry(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "cfg", "repos.json")
	if got, want := DiscoveryStatePath(registryPath), filepath.Join(filepath.Dir(registryPath), "repos.json.discovery.json"); got != want {
		t.Fatalf("path = %s, want %s", got, want)
	}
	for _, name := range []string{"discovery.json", "discovery-status.json", "discovery-cancel.json", "discovery-scan.lock"} {
		registryPath := filepath.Join(t.TempDir(), name)
		if DiscoveryStatePath(registryPath) == registryPath {
			t.Fatalf("custom registry aliases its discovery state: %s", registryPath)
		}
	}
}

func TestDiscoveryStatePerRootMergeAndRemovedRootPruning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rootA := filepath.Join(home, "A")
	rootB := filepath.Join(home, "B")
	for _, root := range []string{rootA, rootB} {
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resultA := DiscoveryResult{ID: discoveryID(filepath.Join(rootA, "repo")), Path: filepath.Join(rootA, "repo"), DiscoveryRoot: rootA}
	resultB := DiscoveryResult{ID: discoveryID(filepath.Join(rootB, "repo")), Path: filepath.Join(rootB, "repo"), DiscoveryRoot: rootB}
	state := NewDiscoveryState()
	if err := state.SetApprovedRoots([]string{rootA, rootB}); err != nil {
		t.Fatal(err)
	}
	state.SetLatest(DiscoveryOutcome{Results: []DiscoveryResult{resultA, resultB}, Summary: DiscoverySummary{FinishedAt: "2026-07-13T12:00:00Z"}})

	updatedA := resultA
	updatedA.Name = "updated"
	state.MergeLatestForRoots(DiscoveryOutcome{Results: []DiscoveryResult{updatedA}, Summary: DiscoverySummary{FinishedAt: "2026-07-13T12:01:00Z"}}, []string{rootA})
	if len(state.LatestResults) != 2 {
		t.Fatalf("per-root rescan erased another root: %#v", state.LatestResults)
	}
	byID := map[string]DiscoveryResult{}
	for _, result := range state.LatestResults {
		byID[result.ID] = result
	}
	if byID[resultA.ID].Name != "updated" || byID[resultB.ID].Path != resultB.Path {
		t.Fatalf("per-root merge = %#v", state.LatestResults)
	}

	if err := state.SetApprovedRoots([]string{rootA}); err != nil {
		t.Fatal(err)
	}
	if len(state.LatestResults) != 1 || state.LatestResults[0].ID != resultA.ID {
		t.Fatalf("removed root results were retained: %#v", state.LatestResults)
	}
}

func TestDiscoveryStatePerRootMergeDropsOverlappingStaleResult(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Join(home, "Code")
	child := filepath.Join(parent, "nested")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(child, "removed-repo")
	state := NewDiscoveryState()
	if err := state.SetApprovedRoots([]string{parent, child}); err != nil {
		t.Fatal(err)
	}
	state.SetLatest(DiscoveryOutcome{Results: []DiscoveryResult{{ID: discoveryID(stalePath), Path: stalePath, DiscoveryRoot: child}}, Summary: DiscoverySummary{FinishedAt: "2026-07-13T12:00:00Z"}})
	state.MergeLatestForRoots(DiscoveryOutcome{Results: []DiscoveryResult{}, Summary: DiscoverySummary{FinishedAt: "2026-07-13T12:01:00Z"}}, []string{parent})
	if len(state.LatestResults) != 0 {
		t.Fatalf("overlapping-root stale result retained: %#v", state.LatestResults)
	}
}
