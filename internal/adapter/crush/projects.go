package crush

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// crushProjectEntry is one row in the Crush projects registry.
type crushProjectEntry struct {
	Path         string `json:"path"`
	DataDir      string `json:"data_dir"`
	LastAccessed string `json:"last_accessed"`
}

// crushProjectsFile is the JSON shape of ~/.local/share/crush/projects.json.
type crushProjectsFile struct {
	Projects []crushProjectEntry `json:"projects"`
}

// projectDB pairs a project's database path with its working-directory
// path so callers that open a crush.db can recover the project root
// the session ran in.
type projectDB struct {
	DBPath      string
	ProjectPath string
}

// loadProjectDBs reads the Crush projects registry and returns every
// project's crush.db path that exists on disk, de-duplicated. The
// registry lives at <global-data-dir>/projects.json; when it is
// missing or unreadable the result is nil (the caller falls back to
// the single resolved database).
func loadProjectDBs() []projectDB {
	registry := filepath.Join(DefaultDir(), "projects.json")

	data, err := os.ReadFile(registry)
	if err != nil {
		return nil
	}

	var file crushProjectsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil
	}

	var dbs []projectDB

	seen := map[string]bool{}

	for _, p := range file.Projects {
		if p.DataDir == "" {
			continue
		}

		dbPath := filepath.Join(p.DataDir, dbName)
		if seen[dbPath] {
			continue
		}

		if info, err := os.Stat(dbPath); err == nil && !info.IsDir() && info.Size() > 0 {
			seen[dbPath] = true
			dbs = append(dbs, projectDB{DBPath: dbPath, ProjectPath: p.Path})
		}
	}

	return dbs
}

// projectPathStore caches dbPath → projectPath, populated once from
// the Crush projects registry on first use. It lives on the Adapter
// so each instance has its own cache, isolating tests.
type projectPathStore struct {
	cache sync.Map // dbPath (string) → projectPath (string)
	once  sync.Once
}

func (ps *projectPathStore) init() {
	for _, pdb := range loadProjectDBs() {
		ps.cache.Store(pdb.DBPath, pdb.ProjectPath)
	}
}

// projectPathForDB resolves the project working directory that owns the
// given crush.db path. It consults the adapter's projects.json cache
// when available, then falls back to deriving the path when the
// database lives inside a conventional <project>/.crush/ data
// directory. Returns "" when neither yields a result, or when the only
// candidate is the global data directory (sessions there belong to
// Crush itself, not a user repo).
func (a Adapter) projectPathForDB(dbPath string) string {
	if dbPath == "" {
		return ""
	}

	if a.projects != nil {
		a.projects.once.Do(a.projects.init)

		if v, ok := a.projects.cache.Load(dbPath); ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}

	return inferProjectPath(dbPath)
}

// inferProjectPath derives the project working directory from a
// conventional <project>/.crush/crush.db path. Returns "" when the
// path does not match the convention or points at the global data dir.
func inferProjectPath(dbPath string) string {
	dataDir := filepath.Dir(dbPath)
	if filepath.Base(dataDir) != dataDirName {
		return ""
	}

	projectDir := filepath.Dir(dataDir)
	if globalDir := DefaultDir(); globalDir != "" && projectDir == globalDir {
		return ""
	}

	return projectDir
}
