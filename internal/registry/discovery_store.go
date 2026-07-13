package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DiscoverySchemaVersion = 1

// DiscoveryState is the complete local discovery persistence contract. It
// stores preferences and the latest repository-only result snapshot, never a
// history or list of ordinary directories examined.
type DiscoveryState struct {
	SchemaVersion    int               `json:"schemaVersion"`
	ApprovedRoots    []string          `json:"approvedRoots"`
	CustomExclusions []string          `json:"customExclusions"`
	Options          DiscoveryOptions  `json:"options"`
	HiddenTokens     []string          `json:"hiddenTokens"`
	LatestResults    []DiscoveryResult `json:"latestResults"`
	LastScanTime     string            `json:"lastScanTime,omitempty"`
	LastSummary      *DiscoverySummary `json:"lastSummary,omitempty"`
}

func NewDiscoveryState() *DiscoveryState {
	return &DiscoveryState{
		SchemaVersion:    DiscoverySchemaVersion,
		ApprovedRoots:    []string{},
		CustomExclusions: []string{},
		Options:          DefaultDiscoveryOptions(),
		HiddenTokens:     []string{},
		LatestResults:    []DiscoveryResult{},
	}
}

// DiscoveryStatePath keeps discovery preferences beside the existing owner
// registry so a custom -config path automatically isolates both files.
func DiscoveryStatePath(registryPath string) string {
	base := filepath.Base(registryPath)
	return filepath.Join(filepath.Dir(registryPath), base+".discovery.json")
}

func LoadDiscoveryState(path string) (*DiscoveryState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewDiscoveryState(), nil
	}
	if err != nil {
		return nil, err
	}
	var state DiscoveryState
	if err := json.Unmarshal(b, &state); err != nil {
		return nil, fmt.Errorf("discovery state %s: %w", path, err)
	}
	if state.SchemaVersion != DiscoverySchemaVersion {
		return nil, fmt.Errorf("discovery state %s: schemaVersion %d, want %d", path, state.SchemaVersion, DiscoverySchemaVersion)
	}
	if options, err := normalizeDiscoveryOptions(state.Options); err != nil {
		return nil, fmt.Errorf("discovery state %s: %w", path, err)
	} else {
		state.Options = options
	}
	if err := validateDiscoveryState(&state); err != nil {
		return nil, fmt.Errorf("discovery state %s: %w", path, err)
	}
	state.normalize()
	return &state, nil
}

func (s *DiscoveryState) Save(path string) error {
	if s == nil {
		return fmt.Errorf("nil discovery state")
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = DiscoverySchemaVersion
	}
	options, err := normalizeDiscoveryOptions(s.Options)
	if err != nil {
		return err
	}
	s.Options = options
	if err := validateDiscoveryState(s); err != nil {
		return err
	}
	s.normalize()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".discovery-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *DiscoveryState) SetOptions(options DiscoveryOptions) error {
	normalized, err := normalizeDiscoveryOptions(options)
	if err != nil {
		return err
	}
	s.Options = normalized
	return nil
}

func (s *DiscoveryState) SetApprovedRoots(paths []string, protectedPaths ...string) error {
	roots := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		root, err := CanonicalScanRoot(path, protectedPaths...)
		if err != nil {
			return err
		}
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	s.ApprovedRoots = roots
	approved := make(map[string]bool, len(roots))
	for _, root := range roots {
		approved[root] = true
	}
	kept := s.LatestResults[:0]
	for _, result := range s.LatestResults {
		if approved[result.DiscoveryRoot] && pathWithin(result.DiscoveryRoot, result.Path) {
			kept = append(kept, result)
		}
	}
	s.LatestResults = kept
	return nil
}

func (s *DiscoveryState) SetCustomExclusions(exclusions []string) error {
	clean := make([]string, 0, len(exclusions))
	seen := map[string]bool{}
	for _, exclusion := range exclusions {
		exclusion = strings.TrimSpace(exclusion)
		if exclusion == "" {
			continue
		}
		if filepath.IsAbs(exclusion) {
			exclusion = filepath.Clean(exclusion)
		} else if filepath.Base(exclusion) != exclusion || exclusion == "." || exclusion == ".." {
			return fmt.Errorf("invalid custom exclusion %q", exclusion)
		}
		if !seen[exclusion] {
			seen[exclusion] = true
			clean = append(clean, exclusion)
		}
	}
	sort.Strings(clean)
	s.CustomExclusions = clean
	return nil
}

func (s *DiscoveryState) Hide(ids ...string) error   { return s.setHidden(true, ids...) }
func (s *DiscoveryState) Unhide(ids ...string) error { return s.setHidden(false, ids...) }

func (s *DiscoveryState) setHidden(hidden bool, ids ...string) error {
	set := map[string]bool{}
	for _, id := range s.HiddenTokens {
		set[id] = true
	}
	for _, id := range ids {
		if !validDiscoveryID(id) {
			return fmt.Errorf("invalid discovery id %q", id)
		}
		if hidden {
			set[id] = true
		} else {
			delete(set, id)
		}
	}
	s.HiddenTokens = s.HiddenTokens[:0]
	for id := range set {
		s.HiddenTokens = append(s.HiddenTokens, id)
	}
	sort.Strings(s.HiddenTokens)
	for i := range s.LatestResults {
		s.LatestResults[i].Hidden = set[s.LatestResults[i].ID]
	}
	return nil
}

func (s *DiscoveryState) SetLatest(outcome DiscoveryOutcome) {
	s.LatestResults = append([]DiscoveryResult(nil), outcome.Results...)
	s.setLatestSummary(outcome.Summary)
}

// MergeLatestForRoots replaces results produced by the roots in this scan
// while retaining the latest results from other still-approved roots. This is
// what makes a per-root rescan local to that root instead of erasing the
// owner's review queue for every other configured root.
func (s *DiscoveryState) MergeLatestForRoots(outcome DiscoveryOutcome, scannedRoots []string) {
	scanned := make(map[string]bool, len(scannedRoots))
	for _, root := range scannedRoots {
		scanned[root] = true
	}
	approved := make(map[string]bool, len(s.ApprovedRoots))
	for _, root := range s.ApprovedRoots {
		approved[root] = true
	}
	byID := make(map[string]DiscoveryResult, len(outcome.Results)+len(s.LatestResults))
	for _, result := range outcome.Results {
		byID[result.ID] = result
	}
	for _, result := range s.LatestResults {
		inScannedRoot := false
		for root := range scanned {
			if pathWithin(root, result.Path) {
				inScannedRoot = true
				break
			}
		}
		if inScannedRoot || !approved[result.DiscoveryRoot] {
			continue
		}
		if _, replaced := byID[result.ID]; !replaced {
			byID[result.ID] = result
		}
	}
	s.LatestResults = make([]DiscoveryResult, 0, len(byID))
	for _, result := range byID {
		s.LatestResults = append(s.LatestResults, result)
	}
	s.setLatestSummary(outcome.Summary)
}

func (s *DiscoveryState) setLatestSummary(summary DiscoverySummary) {
	s.LastSummary = &summary
	if summary.FinishedAt != "" {
		s.LastScanTime = summary.FinishedAt
	} else {
		s.LastScanTime = time.Now().UTC().Format(time.RFC3339)
	}
	s.normalize()
}

// ForgetScanHistory removes the latest repository snapshot and summary while
// preserving approved roots, exclusions, options, and recoverable hidden IDs.
func (s *DiscoveryState) ForgetScanHistory() {
	s.LatestResults = nil
	s.LastSummary = nil
	s.LastScanTime = ""
}

func (s *DiscoveryState) IsHidden(id string) bool {
	for _, hidden := range s.HiddenTokens {
		if hidden == id {
			return true
		}
	}
	return false
}

func validateDiscoveryState(s *DiscoveryState) error {
	if s.SchemaVersion != DiscoverySchemaVersion {
		return fmt.Errorf("invalid discovery schema version")
	}
	for _, root := range s.ApprovedRoots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return fmt.Errorf("approved root is not canonical: %s", root)
		}
	}
	for _, id := range s.HiddenTokens {
		if !validDiscoveryID(id) {
			return fmt.Errorf("invalid hidden discovery id %q", id)
		}
	}
	for _, exclusion := range s.CustomExclusions {
		if filepath.IsAbs(exclusion) {
			if filepath.Clean(exclusion) != exclusion {
				return fmt.Errorf("custom exclusion is not canonical: %s", exclusion)
			}
		} else if filepath.Base(exclusion) != exclusion || exclusion == "." || exclusion == ".." || strings.TrimSpace(exclusion) == "" {
			return fmt.Errorf("invalid custom exclusion %q", exclusion)
		}
	}
	for _, result := range s.LatestResults {
		if !filepath.IsAbs(result.Path) || filepath.Clean(result.Path) != result.Path || discoveryID(result.Path) != result.ID {
			return fmt.Errorf("invalid discovery result %q", result.ID)
		}
	}
	if s.LastScanTime != "" {
		if _, err := time.Parse(time.RFC3339, s.LastScanTime); err != nil {
			return fmt.Errorf("invalid last scan time: %w", err)
		}
	}
	return nil
}

func (s *DiscoveryState) normalize() {
	if s.ApprovedRoots == nil {
		s.ApprovedRoots = []string{}
	}
	if s.CustomExclusions == nil {
		s.CustomExclusions = []string{}
	}
	if s.HiddenTokens == nil {
		s.HiddenTokens = []string{}
	}
	if s.LatestResults == nil {
		s.LatestResults = []DiscoveryResult{}
	}
	sort.Strings(s.ApprovedRoots)
	sort.Strings(s.CustomExclusions)
	sort.Strings(s.HiddenTokens)
	sort.Slice(s.LatestResults, func(i, j int) bool { return s.LatestResults[i].Path < s.LatestResults[j].Path })
	hidden := map[string]bool{}
	for _, id := range s.HiddenTokens {
		hidden[id] = true
	}
	for i := range s.LatestResults {
		s.LatestResults[i].Hidden = hidden[s.LatestResults[i].ID]
	}
}

func validDiscoveryID(id string) bool {
	if len(id) != len("disc_")+32 || !strings.HasPrefix(id, "disc_") {
		return false
	}
	for _, c := range id[len("disc_"):] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
