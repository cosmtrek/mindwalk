package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cosmtrek/mindwalk/internal/registry"
)

type discoveryManager struct {
	mu           sync.Mutex
	registryPath string
	statePath    string
	protected    []string
	progress     registry.DiscoveryProgress
	started      time.Time
	cancel       context.CancelFunc
	active       bool
	ownerLock    *registry.OwnerLock
}

func newDiscoveryManager(registryPath, dataRoot string) *discoveryManager {
	protected := []string{filepath.Dir(registryPath)}
	if strings.TrimSpace(dataRoot) != "" {
		protected = append(protected, dataRoot)
	}
	return &discoveryManager{
		registryPath: registryPath,
		statePath:    registry.DiscoveryStatePath(registryPath),
		protected:    protected,
		progress:     registry.DiscoveryProgress{Status: "idle"},
	}
}

type discoveryConfigResponse struct {
	SchemaVersion    int                           `json:"schemaVersion"`
	ApprovedRoots    []string                      `json:"approvedRoots"`
	SuggestedRoots   []string                      `json:"suggestedRoots"`
	HomeRoot         string                        `json:"homeRoot,omitempty"`
	CustomExclusions []string                      `json:"customExclusions"`
	Options          registry.DiscoveryOptions     `json:"options"`
	Exclusions       []registry.DiscoveryExclusion `json:"exclusions"`
	FollowSymlinks   bool                          `json:"followSymlinks"`
	LastScanAt       string                        `json:"lastScanAt,omitempty"`
	LastScanSummary  *registry.DiscoverySummary    `json:"lastScanSummary,omitempty"`
}

type discoveryConfigMutation struct {
	ApprovedRoots    []string                  `json:"approvedRoots"`
	CustomExclusions []string                  `json:"customExclusions"`
	Options          registry.DiscoveryOptions `json:"options"`
}

type discoveryStartRequest struct {
	Roots   []string                  `json:"roots"`
	Options registry.DiscoveryOptions `json:"options"`
}

type discoveryHideRequest struct {
	IDs    []string `json:"ids"`
	Hidden bool     `json:"hidden"`
}

type discoveryRegistration struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Group   string   `json:"group"`
	Tags    []string `json:"tags"`
	Color   string   `json:"color"`
	Enabled bool     `json:"enabled"`
}

type discoveryRegisterRequest struct {
	Repositories []discoveryRegistration `json:"repositories"`
}

type discoveryRegistrationResult struct {
	ID         string           `json:"id"`
	Status     string           `json:"status"`
	Repository *registry.Status `json:"repository,omitempty"`
	Error      string           `json:"error,omitempty"`
}

func (m *discoveryManager) config() (discoveryConfigResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := registry.LoadDiscoveryState(m.statePath)
	if err != nil {
		return discoveryConfigResponse{}, err
	}
	home, _ := os.UserHomeDir()
	canonicalHome, _ := registry.CanonicalScanRoot(home, m.protected...)
	return discoveryConfigResponse{
		SchemaVersion:    state.SchemaVersion,
		ApprovedRoots:    append([]string{}, state.ApprovedRoots...),
		SuggestedRoots:   suggestedDiscoveryRoots(home, m.protected),
		HomeRoot:         canonicalHome,
		CustomExclusions: append([]string{}, state.CustomExclusions...),
		Options:          state.Options,
		Exclusions:       registry.DefaultDiscoveryExclusions(home, m.protected...),
		FollowSymlinks:   false,
		LastScanAt:       state.LastScanTime,
		LastScanSummary:  state.LastSummary,
	}, nil
}

func suggestedDiscoveryRoots(home string, protected []string) []string {
	if strings.TrimSpace(home) == "" {
		return []string{}
	}
	candidates := []string{home, filepath.Join(home, "Projects"), filepath.Join(home, "Code"), filepath.Join(home, "Documents")}
	out := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		root, err := registry.CanonicalScanRoot(candidate, protected...)
		if err == nil && !seen[root] {
			seen[root] = true
			out = append(out, root)
		}
	}
	return out
}

func (m *discoveryManager) updateConfig(update discoveryConfigMutation) (discoveryConfigResponse, error) {
	m.mu.Lock()
	if m.active {
		m.mu.Unlock()
		return discoveryConfigResponse{}, errors.New("cannot change discovery configuration while a scan is running")
	}
	ownerLock, lockErr := registry.AcquireOwnerLock(m.registryPath)
	if lockErr != nil {
		m.mu.Unlock()
		return discoveryConfigResponse{}, lockErr
	}
	defer ownerLock.Close()
	state, err := registry.LoadDiscoveryState(m.statePath)
	if err == nil {
		err = state.SetApprovedRoots(update.ApprovedRoots, m.protected...)
	}
	if err == nil {
		err = state.SetCustomExclusions(update.CustomExclusions)
	}
	if err == nil {
		err = state.SetOptions(update.Options)
	}
	if err == nil {
		err = state.Save(m.statePath)
	}
	m.mu.Unlock()
	if err != nil {
		return discoveryConfigResponse{}, err
	}
	return m.config()
}

func (m *discoveryManager) start(request discoveryStartRequest) (registry.DiscoveryProgress, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active {
		return registry.DiscoveryProgress{}, errors.New("a repository discovery scan is already running")
	}
	ownerLock, err := registry.AcquireOwnerLock(m.registryPath)
	if err != nil {
		return registry.DiscoveryProgress{}, err
	}
	keepOwnerLock := false
	defer func() {
		if !keepOwnerLock {
			_ = ownerLock.Close()
		}
	}()
	state, err := registry.LoadDiscoveryState(m.statePath)
	if err != nil {
		return registry.DiscoveryProgress{}, err
	}
	if len(request.Roots) == 0 {
		request.Roots = append([]string(nil), state.ApprovedRoots...)
	}
	approved := map[string]bool{}
	for _, root := range state.ApprovedRoots {
		approved[root] = true
	}
	for i, raw := range request.Roots {
		root, err := registry.CanonicalScanRoot(raw, m.protected...)
		if err != nil || !approved[root] {
			return registry.DiscoveryProgress{}, fmt.Errorf("scan root is not owner-approved: %s", raw)
		}
		request.Roots[i] = root
	}
	if len(request.Roots) == 0 {
		return registry.DiscoveryProgress{}, errors.New("approve at least one scan root before starting")
	}
	options := request.Options
	if options == (registry.DiscoveryOptions{}) {
		options = state.Options
	}
	if err := options.Validate(); err != nil {
		return registry.DiscoveryProgress{}, err
	}
	reg, err := registry.Load(m.registryPath)
	if err != nil {
		return registry.DiscoveryProgress{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.active = true
	m.ownerLock = ownerLock
	keepOwnerLock = true
	m.started = time.Now()
	m.progress = registry.DiscoveryProgress{Status: "running", CurrentRoot: request.Roots[0]}
	scanRequest := registry.DiscoveryScanRequest{
		Roots:            append([]string(nil), request.Roots...),
		CustomExclusions: append([]string(nil), state.CustomExclusions...),
		ProtectedPaths:   append([]string(nil), m.protected...),
		Options:          options,
		Registered:       reg.List(),
		HiddenTokens:     append([]string(nil), state.HiddenTokens...),
	}
	scanRequest.OnProgress = func(progress registry.DiscoveryProgress) {
		// The manager owns the terminal transition. Keeping progress "running"
		// until the repository-only snapshot is durably saved prevents a second
		// scan or registration from entering the finalization window.
		if progress.Status != "running" {
			return
		}
		m.mu.Lock()
		m.progress = progress
		m.mu.Unlock()
	}
	go m.run(ctx, scanRequest)
	return m.progress, nil
}

func (m *discoveryManager) run(ctx context.Context, request registry.DiscoveryScanRequest) {
	outcome, scanErr := (registry.DiscoveryScanner{}).Scan(ctx, request)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel = nil
	if outcome.Summary.Status == "" {
		outcome.Summary.DiscoveryProgress = m.progress
		outcome.Summary.Status = "failed"
		outcome.Summary.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if scanErr != nil && outcome.Summary.Status != "cancelled" && outcome.Summary.Status != "timed_out" {
		outcome.Summary.Status = "failed"
	}
	m.progress = outcome.Summary.DiscoveryProgress
	state, err := registry.LoadDiscoveryState(m.statePath)
	if err == nil {
		state.MergeLatestForRoots(outcome, outcome.ScannedRoots)
		err = state.Save(m.statePath)
	}
	if err != nil {
		m.progress.Status = "failed"
	}
	m.active = false
	ownerLock := m.ownerLock
	m.ownerLock = nil
	_ = ownerLock.Close()
}

func (m *discoveryManager) status() registry.DiscoverySummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	progress := m.progress
	state, stateErr := registry.LoadDiscoveryState(m.statePath)
	if stateErr == nil && state.LastSummary != nil && (progress.Status == "idle" || state.LastSummary.Status == progress.Status) {
		return *state.LastSummary
	}
	if m.active {
		progress.Status = "running"
		progress.ElapsedMillis = time.Since(m.started).Milliseconds()
		return registry.DiscoverySummary{DiscoveryProgress: progress, StartedAt: m.started.UTC().Format(time.RFC3339)}
	}
	return registry.DiscoverySummary{DiscoveryProgress: progress}
}

func (m *discoveryManager) cancelScan() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active || m.cancel == nil {
		return errors.New("no repository discovery scan is running")
	}
	m.cancel()
	return nil
}

func (m *discoveryManager) results(showHidden bool) ([]registry.DiscoveryResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := registry.LoadDiscoveryState(m.statePath)
	if err != nil {
		return nil, err
	}
	reg, err := registry.Load(m.registryPath)
	if err != nil {
		return nil, err
	}
	registered := map[string]bool{}
	for _, repo := range reg.List() {
		registered[repo.Path] = true
	}
	out := make([]registry.DiscoveryResult, 0, len(state.LatestResults))
	for _, result := range state.LatestResults {
		result.AlreadyRegistered = registered[result.Path]
		result.Hidden = state.IsHidden(result.ID)
		if showHidden || !result.Hidden {
			out = append(out, result)
		}
	}
	if out == nil {
		out = []registry.DiscoveryResult{}
	}
	return out, nil
}

func (m *discoveryManager) hide(ids []string, hidden bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active {
		return errors.New("cannot change hidden discoveries while a scan is running")
	}
	ownerLock, err := registry.AcquireOwnerLock(m.registryPath)
	if err != nil {
		return err
	}
	defer ownerLock.Close()
	state, err := registry.LoadDiscoveryState(m.statePath)
	if err != nil {
		return err
	}
	if hidden {
		err = state.Hide(ids...)
	} else {
		err = state.Unhide(ids...)
	}
	if err != nil {
		return err
	}
	return state.Save(m.statePath)
}

func (m *discoveryManager) forget() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active {
		return errors.New("cancel the running scan before forgetting history")
	}
	ownerLock, err := registry.AcquireOwnerLock(m.registryPath)
	if err != nil {
		return err
	}
	defer ownerLock.Close()
	state, err := registry.LoadDiscoveryState(m.statePath)
	if err != nil {
		return err
	}
	state.ForgetScanHistory()
	m.progress = registry.DiscoveryProgress{Status: "idle"}
	return state.Save(m.statePath)
}

func (m *discoveryManager) resetExclusions() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active {
		return errors.New("cannot reset exclusions while a scan is running")
	}
	ownerLock, err := registry.AcquireOwnerLock(m.registryPath)
	if err != nil {
		return err
	}
	defer ownerLock.Close()
	state, err := registry.LoadDiscoveryState(m.statePath)
	if err != nil {
		return err
	}
	if err := state.SetCustomExclusions(nil); err != nil {
		return err
	}
	return state.Save(m.statePath)
}

func (s *Server) handleDiscoveryConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		config, err := s.discovery.config()
		if err != nil {
			http.Error(w, "discovery configuration unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(w, config)
	case http.MethodPut:
		if !allowMutation(w, r) {
			return
		}
		var update discoveryConfigMutation
		if !decodeBoundedJSON(w, r, &update) {
			return
		}
		config, err := s.discovery.updateConfig(update)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, config)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDiscoveryStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !allowMutation(w, r) {
		return
	}
	var request discoveryStartRequest
	if !decodeBoundedJSON(w, r, &request) {
		return
	}
	progress, err := s.discovery.start(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, progress)
}

func (s *Server) handleDiscoveryStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.discovery.status())
}

func (s *Server) handleDiscoveryCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !allowMutation(w, r) {
		return
	}
	if err := s.discovery.cancelScan(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]string{"status": "cancelling"})
}

func (s *Server) handleDiscoveryResults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	results, err := s.discovery.results(r.URL.Query().Get("showHidden") == "1")
	if err != nil {
		http.Error(w, "discovery results unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, results)
}

func (s *Server) handleDiscoveryHide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !allowMutation(w, r) {
		return
	}
	var request discoveryHideRequest
	if !decodeBoundedJSON(w, r, &request) {
		return
	}
	if err := s.discovery.hide(request.IDs, request.Hidden); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDiscoveryForget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !allowMutation(w, r) {
		return
	}
	if err := s.discovery.forget(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDiscoveryResetExclusions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !allowMutation(w, r) {
		return
	}
	if err := s.discovery.resetExclusions(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDiscoveryRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !allowMutation(w, r) {
		return
	}
	var request discoveryRegisterRequest
	if !decodeBoundedJSON(w, r, &request) {
		return
	}
	if len(request.Repositories) == 0 || len(request.Repositories) > 500 {
		http.Error(w, "select between 1 and 500 repositories", http.StatusBadRequest)
		return
	}
	s.discovery.mu.Lock()
	defer s.discovery.mu.Unlock()
	if s.discovery.active {
		http.Error(w, "cancel or finish the active discovery scan before registering results", http.StatusConflict)
		return
	}
	ownerLock, err := registry.AcquireOwnerLock(s.cfg.RegistryPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer ownerLock.Close()
	s.registryMu.Lock()
	results := s.registerDiscoveries(request.Repositories)
	s.registryMu.Unlock()
	writeJSON(w, results)
}

func (s *Server) registerDiscoveries(requests []discoveryRegistration) []discoveryRegistrationResult {
	state, err := registry.LoadDiscoveryState(s.discovery.statePath)
	if err != nil {
		return []discoveryRegistrationResult{{Status: "failed", Error: "discovery state unavailable"}}
	}
	byID := map[string]registry.DiscoveryResult{}
	for _, result := range state.LatestResults {
		byID[result.ID] = result
	}
	reg, err := registry.Load(s.cfg.RegistryPath)
	if err != nil {
		return []discoveryRegistrationResult{{Status: "failed", Error: "repository registry unavailable"}}
	}
	out := make([]discoveryRegistrationResult, 0, len(requests))
	for _, request := range requests {
		result := discoveryRegistrationResult{ID: request.ID}
		discovered, ok := byID[request.ID]
		if !ok {
			result.Status, result.Error = "failed", "discovery result is unavailable; rescan before adding"
			out = append(out, result)
			continue
		}
		if discovered.Hidden || !discovered.Accessible || discovered.Type == registry.DiscoveryTypeBroken {
			result.Status, result.Error = "failed", "discovery is hidden or inaccessible"
			out = append(out, result)
			continue
		}
		if !withinAnyApprovedRoot(state.ApprovedRoots, discovered.Path) {
			result.Status, result.Error = "failed", "repository no longer passes approved-root validation"
			out = append(out, result)
			continue
		}
		if validationErr := registry.ValidateDiscoveryCandidate(discovered.Path, discovered.Type, state.ApprovedRoots); validationErr != nil {
			result.Status, result.Error = "failed", "repository Git metadata changed or no longer passes approved-root validation"
			out = append(out, result)
			continue
		}
		canonical := discovered.Path
		if validationErr := registry.ValidateCanonicalRepoPath(canonical); validationErr != nil || !withinAnyApprovedRoot(state.ApprovedRoots, canonical) {
			result.Status, result.Error = "failed", "repository no longer passes approved-root validation"
			out = append(out, result)
			continue
		}
		name := strings.TrimSpace(request.Name)
		if name == "" {
			name = discovered.Name
		}
		repo, addErr := reg.AddValidatedDiscovery(canonical, name, discovered.Type, state.ApprovedRoots)
		if addErr == nil && (repo.Path != canonical || !withinAnyApprovedRoot(state.ApprovedRoots, repo.Path)) {
			_ = reg.Remove(repo.ID)
			addErr = errors.New("repository path changed during final registration")
		}
		if errors.Is(addErr, registry.ErrExists) {
			result.Status = "already_registered"
			out = append(out, result)
			continue
		}
		if addErr == nil {
			addErr = reg.Update(repo.ID, name, strings.TrimSpace(request.Group), strings.TrimSpace(request.Color), cleanTags(request.Tags))
		}
		if addErr == nil {
			addErr = reg.SetEnabled(repo.ID, request.Enabled)
		}
		if addErr == nil {
			addErr = reg.Save()
		}
		if addErr != nil {
			_ = reg.Remove(repo.ID)
			result.Status, result.Error = "failed", addErr.Error()
			out = append(out, result)
			continue
		}
		status, statusErr := reg.StatusOf(repo.ID)
		if statusErr != nil {
			result.Status, result.Error = "failed", "repository added but status is unavailable"
		} else {
			result.Status, result.Repository = "added", &status
		}
		out = append(out, result)
	}
	return out
}

func withinAnyApprovedRoot(roots []string, path string) bool {
	for _, root := range roots {
		if registry.WithinCanonical(root, path) {
			return true
		}
	}
	return false
}
