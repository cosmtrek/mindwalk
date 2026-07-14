package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const stateFileVersion = 1

type stateFile struct {
	SchemaVersion int              `json:"schemaVersion"`
	Sources       map[string]State `json:"sources"`
}

// StateStore atomically persists tail offsets beneath one private local data
// root. Source files remain read-only and may live elsewhere; only offsets,
// file identity anchors, and paths are stored here.
type StateStore struct {
	path string
	mu   sync.Mutex
}

// NewStateStore creates a root-confined state store. The root is created with
// mode 0700 and canonicalized before the state path is fixed.
func NewStateStore(dataRoot string) (*StateStore, error) {
	if dataRoot == "" {
		return nil, errors.New("ingest data root is required")
	}
	abs, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return &StateStore{path: filepath.Join(root, "tail-state.json")}, nil
}

// Load returns the persisted state for sourceID.
func (s *StateStore) Load(sourceID string) (State, bool, error) {
	if sourceID == "" {
		return State{}, false, errors.New("source id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return State{}, false, err
	}
	state, ok := f.Sources[sourceID]
	return state, ok, nil
}

// List returns a defensive copy of all persisted resume points. It is used
// to detect files that disappeared between directory scans.
func (s *StateStore) List() (map[string]State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make(map[string]State, len(f.Sources))
	for id, state := range f.Sources {
		out[id] = state
	}
	return out, nil
}

// Save atomically records a source's resume point with 0600 permissions.
func (s *StateStore) Save(sourceID string, state State) error {
	if sourceID == "" {
		return errors.New("source id is required")
	}
	if err := Resume(state).validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	f.Sources[sourceID] = state
	return s.write(f)
}

// Delete forgets one resume point without touching its source file.
func (s *StateStore) Delete(sourceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	delete(f.Sources, sourceID)
	return s.write(f)
}

func (s *StateStore) read() (stateFile, error) {
	if info, err := os.Lstat(s.path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return stateFile{}, errors.New("tail state file must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return stateFile{}, err
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return stateFile{SchemaVersion: stateFileVersion, Sources: map[string]State{}}, nil
	}
	if err != nil {
		return stateFile{}, err
	}
	var f stateFile
	if err := json.Unmarshal(b, &f); err != nil {
		return stateFile{}, fmt.Errorf("tail state: %w", err)
	}
	if f.SchemaVersion != stateFileVersion || f.Sources == nil {
		return stateFile{}, fmt.Errorf("unsupported tail state schemaVersion %d", f.SchemaVersion)
	}
	for id, state := range f.Sources {
		if id == "" {
			return stateFile{}, errors.New("tail state contains an empty source id")
		}
		if err := Resume(state).validate(); err != nil {
			return stateFile{}, fmt.Errorf("tail state %s: %w", id, err)
		}
	}
	return f, nil
}

func (s *StateStore) write(f stateFile) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".tail-state-*.tmp")
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
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
