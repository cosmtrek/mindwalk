package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmtrek/mindwalk/internal/registry"
)

func TestDiscoveryAPIExplicitScanHideRegisterAndRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "Projects")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	repoA := createDiscoveryGitRepo(t, root, "alpha")
	_ = createDiscoveryGitRepo(t, root, "beta")
	configPath := filepath.Join(home, ".config", "mindwalk-test", "repos.json")
	s := New(Config{RegistryPath: configPath, DataRoot: filepath.Join(home, ".local", "share", "mindwalk-test")})
	h := s.Handler()

	initial := apiRequest(t, h, http.MethodGet, "/api/repository-discovery/status", "", false)
	if initial.Code != http.StatusOK || !jsonBodyContains(initial.Body.Bytes(), `"status": "idle"`) {
		t.Fatalf("initial status = %d %s", initial.Code, initial.Body.String())
	}
	initialConfig := apiRequest(t, h, http.MethodGet, "/api/repository-discovery/config", "", false)
	if !jsonBodyContains(initialConfig.Body.Bytes(), `"approvedRoots": []`) || !jsonBodyContains(initialConfig.Body.Bytes(), `"customExclusions": []`) {
		t.Fatalf("empty discovery config arrays must not be null: %s", initialConfig.Body.String())
	}
	if repositories := apiRequest(t, h, http.MethodGet, "/api/repositories", "", false); repositories.Body.String() != "[]\n" {
		t.Fatalf("discovery registered automatically: %s", repositories.Body.String())
	}

	options := registry.DefaultDiscoveryOptions()
	options.MaxDepth = 8
	options.MaxDirectories = 5000
	options.MaxResults = 100
	options.TimeoutSeconds = 10
	configBody := fmt.Sprintf(`{"approvedRoots":[%s],"customExclusions":["scratch"],"options":{"maxDepth":%d,"maxDirectories":%d,"maxResults":%d,"timeoutSeconds":%d,"findNested":false}}`, jsonString(root), options.MaxDepth, options.MaxDirectories, options.MaxResults, options.TimeoutSeconds)
	if denied := apiRequest(t, h, http.MethodPut, "/api/repository-discovery/config", configBody, false); denied.Code != http.StatusForbidden {
		t.Fatalf("config without CSRF = %d", denied.Code)
	}
	saved := apiRequest(t, h, http.MethodPut, "/api/repository-discovery/config", configBody, true)
	if saved.Code != http.StatusOK {
		t.Fatalf("save config = %d %s", saved.Code, saved.Body.String())
	}
	if info, err := os.Stat(registry.DiscoveryStatePath(configPath)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("discovery state mode = %v err=%v", info, err)
	}

	unapproved := apiRequest(t, h, http.MethodPost, "/api/repository-discovery/start", `{"roots":["/tmp"]}`, true)
	if unapproved.Code != http.StatusBadRequest {
		t.Fatalf("unapproved root = %d %s", unapproved.Code, unapproved.Body.String())
	}
	started := apiRequest(t, h, http.MethodPost, "/api/repository-discovery/start", `{"roots":[`+jsonString(root)+`]}`, true)
	if started.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", started.Code, started.Body.String())
	}
	waitForDiscoveryState(t, h, "completed", "bounded")

	resultsResponse := apiRequest(t, h, http.MethodGet, "/api/repository-discovery/results", "", false)
	var results []registry.DiscoveryResult
	if err := json.Unmarshal(resultsResponse.Body.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	if repositories := apiRequest(t, h, http.MethodGet, "/api/repositories", "", false); repositories.Body.String() != "[]\n" {
		t.Fatalf("scan registered automatically: %s", repositories.Body.String())
	}

	hide := apiRequest(t, h, http.MethodPost, "/api/repository-discovery/hide", `{"ids":["`+results[1].ID+`"],"hidden":true}`, true)
	if hide.Code != http.StatusNoContent {
		t.Fatalf("hide = %d %s", hide.Code, hide.Body.String())
	}
	visible := apiRequest(t, h, http.MethodGet, "/api/repository-discovery/results", "", false)
	var visibleResults []registry.DiscoveryResult
	_ = json.Unmarshal(visible.Body.Bytes(), &visibleResults)
	if len(visibleResults) != 1 {
		t.Fatalf("visible results = %#v", visibleResults)
	}
	all := apiRequest(t, h, http.MethodGet, "/api/repository-discovery/results?showHidden=1", "", false)
	var allResults []registry.DiscoveryResult
	_ = json.Unmarshal(all.Body.Bytes(), &allResults)
	if len(allResults) != 2 || !allResults[1].Hidden {
		t.Fatalf("hidden recovery = %#v", allResults)
	}
	unhide := apiRequest(t, h, http.MethodPost, "/api/repository-discovery/hide", `{"ids":["`+results[1].ID+`"],"hidden":false}`, true)
	if unhide.Code != http.StatusNoContent {
		t.Fatalf("unhide = %d", unhide.Code)
	}

	pathInjection := apiRequest(t, h, http.MethodPost, "/api/repository-discovery/register", `{"repositories":[{"id":"`+results[0].ID+`","path":`+jsonString(repoA)+`,"name":"injected","group":"","tags":[],"color":"","enabled":true}]}`, true)
	if pathInjection.Code != http.StatusBadRequest {
		t.Fatalf("register accepted an arbitrary path = %d %s", pathInjection.Code, pathInjection.Body.String())
	}

	unknownID := "disc_00000000000000000000000000000000"
	registerBody := `{"repositories":[{"id":"` + results[0].ID + `","name":"Approved Alpha","group":"fixtures","tags":["selected"],"color":"cyan","enabled":true},{"id":"` + unknownID + `","name":"Unavailable","group":"","tags":[],"color":"","enabled":true}]}`
	registered := apiRequest(t, h, http.MethodPost, "/api/repository-discovery/register", registerBody, true)
	if registered.Code != http.StatusOK || !jsonBodyContains(registered.Body.Bytes(), `"status": "added"`) || !jsonBodyContains(registered.Body.Bytes(), `"status": "failed"`) {
		t.Fatalf("register = %d %s", registered.Code, registered.Body.String())
	}
	reg, err := registry.Load(configPath)
	if err != nil || len(reg.List()) != 1 || reg.List()[0].Path != repoA || reg.List()[0].Name != "Approved Alpha" {
		t.Fatalf("registry = %#v err=%v", reg.List(), err)
	}

	restarted := New(Config{RegistryPath: configPath, DataRoot: filepath.Join(home, ".local", "share", "mindwalk-test")})
	restartedConfig := apiRequest(t, restarted.Handler(), http.MethodGet, "/api/repository-discovery/config", "", false)
	var persisted discoveryConfigResponse
	if err := json.Unmarshal(restartedConfig.Body.Bytes(), &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.ApprovedRoots) != 1 || persisted.ApprovedRoots[0] != root || len(persisted.CustomExclusions) != 1 {
		t.Fatalf("persisted config = %#v", persisted)
	}
	status := apiRequest(t, restarted.Handler(), http.MethodGet, "/api/repository-discovery/status", "", false)
	if jsonBodyContains(status.Body.Bytes(), `"status": "running"`) {
		t.Fatalf("restart automatically rescanned: %s", status.Body.String())
	}
}

func TestDiscoveryAPIMutationsRequireCSRF(t *testing.T) {
	s := New(Config{RegistryPath: filepath.Join(t.TempDir(), "repos.json")})
	h := s.Handler()
	for _, endpoint := range []string{
		"/api/repository-discovery/start",
		"/api/repository-discovery/cancel",
		"/api/repository-discovery/hide",
		"/api/repository-discovery/register",
		"/api/repository-discovery/forget",
		"/api/repository-discovery/reset-exclusions",
	} {
		response := apiRequest(t, h, http.MethodPost, endpoint, `{}`, false)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s without CSRF = %d", endpoint, response.Code)
		}
	}
}

func TestDiscoveryAPIRejectsRegistrationDuringActiveScan(t *testing.T) {
	s := New(Config{RegistryPath: filepath.Join(t.TempDir(), "repos.json")})
	s.discovery.mu.Lock()
	// Simulate the scanner having emitted its terminal callback while the
	// manager still owns final persistence. The explicit active guard must keep
	// every competing mutation out of this window.
	s.discovery.active = true
	s.discovery.started = time.Now()
	s.discovery.progress = registry.DiscoveryProgress{Status: "completed"}
	s.discovery.mu.Unlock()
	response := apiRequest(t, s.Handler(), http.MethodPost, "/api/repository-discovery/register", `{"repositories":[{"id":"disc_00000000000000000000000000000000","name":"blocked","group":"","tags":[],"color":"","enabled":true}]}`, true)
	if response.Code != http.StatusConflict {
		t.Fatalf("registration during scan = %d %s", response.Code, response.Body.String())
	}
	status := apiRequest(t, s.Handler(), http.MethodGet, "/api/repository-discovery/status", "", false)
	if !jsonBodyContains(status.Body.Bytes(), `"status": "running"`) {
		t.Fatalf("active finalization window published terminal status: %s", status.Body.String())
	}
}

func TestDiscoveryAPIPerRootRescanPreservesOtherRootResults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rootA := filepath.Join(home, "A")
	rootB := filepath.Join(home, "B")
	for _, root := range []string{rootA, rootB} {
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		createDiscoveryGitRepo(t, root, "repo")
	}
	configPath := filepath.Join(home, ".config", "mindwalk-test", "repos.json")
	h := New(Config{RegistryPath: configPath, DataRoot: filepath.Join(home, ".local", "share", "mindwalk-test")}).Handler()
	options := registry.DefaultDiscoveryOptions()
	configBody := fmt.Sprintf(`{"approvedRoots":[%s,%s],"customExclusions":[],"options":{"maxDepth":%d,"maxDirectories":%d,"maxResults":%d,"timeoutSeconds":%d,"findNested":false}}`, jsonString(rootA), jsonString(rootB), options.MaxDepth, options.MaxDirectories, options.MaxResults, options.TimeoutSeconds)
	if response := apiRequest(t, h, http.MethodPut, "/api/repository-discovery/config", configBody, true); response.Code != http.StatusOK {
		t.Fatalf("save config = %d %s", response.Code, response.Body.String())
	}
	if response := apiRequest(t, h, http.MethodPost, "/api/repository-discovery/start", `{"roots":[`+jsonString(rootA)+`,`+jsonString(rootB)+`]}`, true); response.Code != http.StatusAccepted {
		t.Fatalf("start all roots = %d %s", response.Code, response.Body.String())
	}
	waitForDiscoveryState(t, h, "completed")
	if response := apiRequest(t, h, http.MethodPost, "/api/repository-discovery/start", `{"roots":[`+jsonString(rootA)+`]}`, true); response.Code != http.StatusAccepted {
		t.Fatalf("rescan one root = %d %s", response.Code, response.Body.String())
	}
	waitForDiscoveryState(t, h, "completed")
	response := apiRequest(t, h, http.MethodGet, "/api/repository-discovery/results", "", false)
	var results []registry.DiscoveryResult
	if err := json.Unmarshal(response.Body.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("per-root rescan erased results from another root: %#v", results)
	}
}

func createDiscoveryGitRepo(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "init", "-q", "-b", "main")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return dir
}

func waitForDiscoveryState(t *testing.T, h http.Handler, states ...string) registry.DiscoveryProgress {
	t.Helper()
	wanted := map[string]bool{}
	for _, state := range states {
		wanted[state] = true
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response := apiRequest(t, h, http.MethodGet, "/api/repository-discovery/status", "", false)
		var progress registry.DiscoveryProgress
		if err := json.Unmarshal(response.Body.Bytes(), &progress); err == nil && wanted[progress.Status] {
			return progress
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("discovery did not reach states %v", states)
	return registry.DiscoveryProgress{}
}

func jsonBodyContains(body []byte, value string) bool {
	return strings.Contains(string(body), value)
}
