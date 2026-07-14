// Package registry manages the explicit, owner-curated set of observable
// repositories. Discovery is a separate, bounded preview: only repositories
// the owner explicitly approves are added to this registry and observed.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SchemaVersion versions the persisted registry file.
const SchemaVersion = 1

var (
	// ErrNotFound marks an unknown repository id or path.
	ErrNotFound = errors.New("repository not registered")
	// ErrExists marks an attempt to re-register a path.
	ErrExists = errors.New("repository already registered")
)

// Repo is one registered repository. Git state is observed live via
// ReadGitMeta, never persisted, so it cannot go stale on disk.
type Repo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Path    string   `json:"path"` // canonical, symlink-free
	Group   string   `json:"group,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Color   string   `json:"color,omitempty"`
	Enabled bool     `json:"enabled"`
	AddedAt string   `json:"addedAt"` // RFC3339 UTC
}

type file struct {
	SchemaVersion int    `json:"schemaVersion"`
	Repos         []Repo `json:"repos"`
}

// Registry is the in-memory registry bound to a config file path.
type Registry struct {
	path  string
	repos map[string]Repo // by id
	now   func() time.Time
}

// DefaultPath returns the per-user registry file location.
func DefaultPath(configDirName string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, configDirName, "repos.json"), nil
}

// Load reads the registry at path, or returns an empty registry when the
// file does not exist yet.
func Load(path string) (*Registry, error) {
	r := &Registry{path: path, repos: map[string]Repo{}, now: time.Now}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("registry %s: %w", path, err)
	}
	if f.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("registry %s: schemaVersion %d, want %d", path, f.SchemaVersion, SchemaVersion)
	}
	for _, repo := range f.Repos {
		if err := validateStoredRepo(repo); err != nil {
			return nil, fmt.Errorf("registry %s: %w", path, err)
		}
		if _, exists := r.repos[repo.ID]; exists {
			return nil, fmt.Errorf("registry %s: duplicate repository id %s", path, repo.ID)
		}
		r.repos[repo.ID] = repo
	}
	return r, nil
}

// Save writes the registry atomically: temp file in the same directory,
// fsync, rename. A crash can never leave a half-written registry.
func (r *Registry) Save() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(file{SchemaVersion: SchemaVersion, Repos: r.List()}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".repos-*.tmp")
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
	if err := os.Rename(tmp.Name(), r.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(r.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func validateStoredRepo(repo Repo) error {
	if repo.ID == "" || repo.Name == "" || repo.Path == "" || repo.AddedAt == "" {
		return fmt.Errorf("invalid repository record %q: required field missing", repo.ID)
	}
	if _, err := time.Parse(time.RFC3339, repo.AddedAt); err != nil {
		return fmt.Errorf("invalid repository %s addedAt: %w", repo.ID, err)
	}
	if !filepath.IsAbs(repo.Path) || filepath.Clean(repo.Path) != repo.Path || repoID(repo.Path) != repo.ID {
		return fmt.Errorf("invalid repository %s: path or id is not canonical", repo.ID)
	}
	if canonical, err := CanonicalRepoPath(repo.Path); err == nil {
		if canonical != repo.Path || repoID(canonical) != repo.ID {
			return fmt.Errorf("invalid repository %s: path or id is not canonical", repo.ID)
		}
	} else if _, statErr := os.Lstat(repo.Path); !errors.Is(statErr, os.ErrNotExist) {
		// Existing unsafe paths (including dangling symlinks) are never loaded.
		return fmt.Errorf("invalid repository %s: %w", repo.ID, err)
	}
	return nil
}

// repoID derives a stable id from the canonical path.
func repoID(canonicalPath string) string {
	sum := sha256.Sum256([]byte(canonicalPath))
	return "repo_" + hex.EncodeToString(sum[:6])
}

// Add registers a repository by path. The path is canonicalized and checked
// against the safety rules; a display name defaults to the directory base.
func (r *Registry) Add(path, name string) (Repo, error) {
	canonical, err := CanonicalRepoPath(path)
	if err != nil {
		return Repo{}, err
	}
	return r.addCanonical(canonical, name)
}

// AddValidatedDiscovery repeats discovery's rooted validation and then stores
// the original canonical path without resolving it again. A filesystem swap
// after validation can make that path unavailable, but it cannot substitute
// an outside target in the registry record.
func (r *Registry) AddValidatedDiscovery(path, name, expectedType string, approvedRoots []string) (Repo, error) {
	inside := false
	for _, root := range approvedRoots {
		if pathWithin(root, path) {
			inside = true
			break
		}
	}
	if !inside {
		return Repo{}, fmt.Errorf("%w: repository is outside approved discovery roots", ErrUnsafePath)
	}
	if err := ValidateCanonicalRepoPath(path); err != nil {
		return Repo{}, err
	}
	if err := ValidateDiscoveryCandidate(path, expectedType, approvedRoots); err != nil {
		return Repo{}, err
	}
	return r.addCanonical(path, name)
}

func (r *Registry) addCanonical(canonical, name string) (Repo, error) {
	if err := ValidateCanonicalRepoPath(canonical); err != nil {
		return Repo{}, err
	}
	id := repoID(canonical)
	if _, ok := r.repos[id]; ok {
		return Repo{}, fmt.Errorf("%w: %s", ErrExists, canonical)
	}
	if name == "" {
		name = filepath.Base(canonical)
	}
	repo := Repo{
		ID:      id,
		Name:    name,
		Path:    canonical,
		Enabled: true,
		AddedAt: r.now().UTC().Format(time.RFC3339),
	}
	r.repos[id] = repo
	return repo, nil
}

// Get returns a repository by id.
func (r *Registry) Get(id string) (Repo, error) {
	repo, ok := r.repos[id]
	if !ok {
		return Repo{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return repo, nil
}

// Remove unregisters a repository. It never touches the repository contents.
func (r *Registry) Remove(id string) error {
	if _, ok := r.repos[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(r.repos, id)
	return nil
}

// SetEnabled toggles observation of a repository without forgetting it.
func (r *Registry) SetEnabled(id string, enabled bool) error {
	repo, ok := r.repos[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	repo.Enabled = enabled
	r.repos[id] = repo
	return nil
}

// Update changes owner-controlled display metadata without touching the
// repository. Empty values clear optional fields; name must remain non-empty.
func (r *Registry) Update(id, name, group, color string, tags []string) error {
	repo, ok := r.repos[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if name == "" {
		return fmt.Errorf("repository name must not be empty")
	}
	repo.Name = name
	repo.Group = group
	repo.Color = color
	repo.Tags = append([]string(nil), tags...)
	sort.Strings(repo.Tags)
	r.repos[id] = repo
	return nil
}

// List returns all repositories ordered by name then id, deterministically.
func (r *Registry) List() []Repo {
	out := make([]Repo, 0, len(r.repos))
	for _, repo := range r.repos {
		out = append(out, repo)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Status pairs a repository with its live-observed state.
type Status struct {
	Repo        Repo    `json:"repo"`
	Missing     bool    `json:"missing"`
	InvalidPath bool    `json:"invalidPath,omitempty"`
	Error       string  `json:"error,omitempty"`
	Git         GitMeta `json:"git"`
}

// StatusOf observes a repository's current on-disk and git state.
func (r *Registry) StatusOf(id string) (Status, error) {
	repo, err := r.Get(id)
	if err != nil {
		return Status{}, err
	}
	canonical, err := CanonicalRepoPath(repo.Path)
	if err != nil {
		if _, statErr := os.Lstat(repo.Path); errors.Is(statErr, os.ErrNotExist) {
			return Status{Repo: repo, Missing: true}, nil
		}
		return Status{Repo: repo, InvalidPath: true, Error: err.Error()}, nil
	}
	if canonical != repo.Path {
		return Status{Repo: repo, InvalidPath: true, Error: "repository path no longer resolves to its registered canonical path"}, nil
	}
	info, err := os.Stat(repo.Path)
	if err != nil || !info.IsDir() {
		return Status{Repo: repo, Missing: true}, nil
	}
	return Status{Repo: repo, Git: ReadGitMeta(repo.Path)}, nil
}
