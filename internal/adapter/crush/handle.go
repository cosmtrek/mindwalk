package crush

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	crushdata "github.com/LarsArtmann/go-crush-data"
)

// dbHandle wraps a read-only crushdata.DB with the database path it
// was opened from so callers can attribute results to a project.
// When cached is true the handle came from the adapter's dbCache and
// close() is a no-op so the connection survives across requests.
type dbHandle struct {
	path   string
	inner  *crushdata.DB
	cached bool
}

// openAt opens the crush.db that lives next to dbPath via the
// go-crush-data SDK. A missing database yields (nil, nil) so callers
// can treat "no Crush data" as an empty catalog instead of an error;
// a database that exists but is broken (empty file, directory in the
// way, missing required tables) surfaces a distinct error naming the
// path so a user can fix the configuration without re-deriving what
// went wrong.
func openAt(dbPath string) (*dbHandle, error) {
	info, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("inspect crush database at %s: %w", dbPath, err)
	}

	if info.IsDir() {
		return nil, fmt.Errorf("crush database path %s is a directory, not a file", dbPath)
	}

	if info.Size() == 0 {
		return nil, fmt.Errorf("crush database at %s is empty (size 0)", dbPath)
	}

	db, err := crushdata.Open(filepath.Dir(dbPath))
	if err != nil {
		if errors.Is(err, crushdata.ErrDatabaseNotFound) {
			return nil, nil
		}

		return nil, err
	}

	return &dbHandle{path: dbPath, inner: db}, nil
}

// close releases the handle unless it came from the adapter cache.
func (h *dbHandle) close() error {
	if h == nil || h.inner == nil || h.cached {
		return nil
	}

	return h.inner.Close()
}

// closeDiscard calls close() and drops its error for defer sites.
func (h *dbHandle) closeDiscard() {
	_ = h.close()
}

// missingColumns reports the well-known columns the database lacks,
// via the SDK's capability probe (sessions.cost, parent_session_id,
// messages.model, provider, finished_at, read_files).
func (h *dbHandle) missingColumns() []string {
	return h.inner.Schema().MissingColumns()
}
