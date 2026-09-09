// Package crush adapts charmbracelet/crush sessions to the shared
// model.Trace contract. Crush stores every session inside a single
// SQLite database (crush.db) under a per-project data directory.
//
// The adapter is read-only and shares the data dir with a running
// crush TUI: it opens the database in mode=ro without acquiring the
// data-dir lock and never writes. Each session's tool calls live in
// one row per message, with parts JSON-encoded in messages.parts and
// ordered by created_at (second-precision Unix timestamps).
package crush

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	crushdata "github.com/LarsArtmann/go-crush-data"
)

const (
	// dataDirName is the conventional Crush project data directory
	// (the schema default in upstream crush).
	dataDirName = ".crush"

	// dbName is the SQLite filename Crush uses for every data dir.
	dbName = "crush.db"

	// appName is the XDG application identifier Crush uses for global
	// data and config directories.
	appName = "crush"
)

type Adapter struct {
	// Dir overrides the Crush data directory. When empty the adapter
	// auto-discovers one by walking upward from the current working
	// directory looking for a .crush/crush.db (bounded by the git
	// worktree root) and then falls back to CRUSH_GLOBAL_DATA,
	// XDG_DATA_HOME, and ~/.local/share/crush.
	Dir string

	// WorkingDir is the directory used to discover a project-local
	// .crush directory. Empty defaults to os.Getwd. Tests inject a
	// temp directory so they never read the host filesystem.
	WorkingDir string

	// dbIndex maps session IDs to their crush.db filesystem path,
	// populated during ListSessions so Parse/Summarize can open the
	// right database. Nil when the adapter was constructed without
	// NewAdapter (e.g. Adapter{Dir: dir}); in that case routing falls
	// back to the single resolved database.
	dbIndex *sync.Map

	// dbCache caches open *crushdata.DB handles by database path so a
	// long-lived server does not re-open the SQLite file on every
	// Parse/Summarize call. Nil when the adapter was constructed
	// without NewAdapter; in that case every call opens and closes its
	// own handle.
	dbCache *sync.Map

	// projects caches the projects.json registry so projectPathForDB
	// can resolve a database path to a project working directory without
	// re-reading the registry on every call. Nil for zero-value
	// Adapters; they fall back to path inference.
	projects *projectPathStore

	// warnedOldSchema deduplicates schema warnings so each database
	// path is warned about at most once per adapter instance.
	warnedOldSchema *sync.Map
}

// NewAdapter creates an Adapter with its own session-to-DB index and
// database connection cache, isolating it from other Adapter instances.
// Use this in the server and in tests that need to control multi-database
// routing without sharing global state.
func NewAdapter(dir string) Adapter {
	return Adapter{
		Dir:             dir,
		dbIndex:         &sync.Map{},
		dbCache:         &sync.Map{},
		projects:        &projectPathStore{},
		warnedOldSchema: &sync.Map{},
	}
}

// Close releases cached database connections. Safe to call multiple
// times; adapters constructed without NewAdapter (nil dbCache) are a
// no-op.
func (a Adapter) Close() error {
	if a.dbCache == nil {
		return nil
	}

	var firstErr error

	a.dbCache.Range(func(_, value any) bool {
		if db, ok := value.(*crushdata.DB); ok && db != nil {
			if err := db.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}

		return true
	})

	return firstErr
}

// DefaultDir returns the platform-specific global Crush data directory:
// CRUSH_GLOBAL_DATA when set, otherwise $XDG_DATA_HOME/crush (Unix) or
// %LOCALAPPDATA%\crush (Windows), otherwise ~/.local/share/crush.
//
// The global directory holds crush.json (machine state); the database
// crush.db lives next to it. Empty when the home directory cannot be
// resolved.
func DefaultDir() string {
	if env := os.Getenv("CRUSH_GLOBAL_DATA"); env != "" {
		return env
	}

	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, appName)
	}

	if runtime.GOOS == "windows" {
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			if home := os.Getenv("USERPROFILE"); home != "" {
				local = filepath.Join(home, "AppData", "Local")
			}
		}

		if local != "" {
			return filepath.Join(local, appName)
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", appName)
	}

	return ""
}

// Harness returns the canonical harness identifier the rest of
// mindwalk uses to filter traces and pick the right adapter.
func (a Adapter) Harness() string { return "crush" }

// SessionDir returns the resolved Crush data directory the
// adapter will use to look up sessions. It returns the explicit
// override first, then the project-local .crush (walking up from
// the working dir, bounded by the git worktree root), then the
// global directory. The returned path may not exist on disk;
// callers should fall back gracefully.
func (a Adapter) SessionDir() string {
	if a.Dir != "" {
		return a.Dir
	}

	workdir := a.WorkingDir
	if workdir == "" {
		if cwd, err := os.Getwd(); err == nil {
			workdir = cwd
		}
	}

	if workdir != "" {
		if resolved := lookupProjectDataDir(workdir); resolved != "" {
			return resolved
		}
	}

	return DefaultDir()
}

// dbPath returns the crush.db path under the resolved data dir, or ""
// when no data dir can be resolved.
func (a Adapter) dbPath() string {
	dir := a.SessionDir()
	if dir == "" {
		return ""
	}

	return filepath.Join(dir, dbName)
}

// lookupProjectDataDir walks from dir upward looking for a .crush
// directory, stopping at the git worktree root when one is detected so
// we never pick up a stranger's Crush database above the project. The
// returned path is absolute and points at the data directory itself
// (the directory that contains crush.db), not the database file.
func lookupProjectDataDir(dir string) string {
	if dir == "" {
		return ""
	}

	boundary := projectBoundary(dir)

	current := dir
	for {
		candidate := filepath.Join(current, dataDirName)
		if isCrushDataDir(candidate) {
			return candidate
		}

		parent := filepath.Dir(current)
		if parent == current || (boundary != "" && current == boundary) {
			return ""
		}

		current = parent
	}
}

// isCrushDataDir reports whether dir is a Crush data directory — a
// directory containing crush.db. The bare .crush project directory
// may exist with no database; ignore it so we don't misidentify an
// unrelated project.
func isCrushDataDir(dir string) bool {
	if dir == "" {
		return false
	}

	info, err := os.Stat(filepath.Join(dir, dbName))

	return err == nil && !info.IsDir()
}

// projectBoundary returns the directory at which an upward
// configuration search rooted at dir should stop — the git worktree
// root when one can be detected, otherwise the directory itself. The
// boundary keeps Crush from silently adopting state files placed above
// the current project.
func projectBoundary(dir string) string {
	if dir == "" {
		return ""
	}

	if root := worktreeRoot(dir); root != "" {
		return root
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}

	return abs
}

// worktreeRootCache memoizes the git worktree root per directory. The
// root is stable for the life of the process, so re-shelling out to
// `git rev-parse` on every adapter query would be wasteful. Keyed by
// the requested dir; the value is the resolved root ("" when dir is
// not in a git worktree).
var worktreeRootCache sync.Map

// worktreeRoot returns the absolute path of the git working tree root
// for dir, or "" when dir is not inside a working tree (bare
// repositories, missing git binary, plain directories, or any other
// failure mode).
func worktreeRoot(dir string) string {
	if v, ok := worktreeRootCache.Load(dir); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}

	root := computeWorktreeRoot(dir)
	worktreeRootCache.Store(dir, root)

	return root
}

func computeWorktreeRoot(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	root := strings.TrimSpace(string(out))
	if root == "" {
		return ""
	}

	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}

	return root
}

// errDBUnavailable is returned by Parse/Summarize when the database
// is missing or unreadable. Callers fall back to "no sessions" when
// the same condition is observed during ListSessions.
var errDBUnavailable = errors.New("crush database not available")
