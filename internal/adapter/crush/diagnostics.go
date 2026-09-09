package crush

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/adapter"
)

// Diagnostics runs health checks on the Crush adapter's configuration
// and every discovered database. It checks:
//
//   - Data directory readability
//   - projects.json registry validity (auto-discover mode)
//   - Schema column coverage on each database
//
// The method is safe to call on a zero-value Adapter (no dbCache);
// each database is opened and closed independently.
func (a Adapter) Diagnostics() []adapter.DiagnosticCheck {
	var checks []adapter.DiagnosticCheck

	dir := a.SessionDir()
	if dir == "" {
		checks = append(checks, adapter.DiagnosticCheck{
			Name:   "data-dir",
			Status: "error",
			Detail: "no Crush data directory could be resolved",
		})

		return checks
	}

	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		checks = append(checks, adapter.DiagnosticCheck{
			Name:   "data-dir",
			Status: "warn",
			Detail: fmt.Sprintf("data directory %s does not exist or is not a directory", dir),
		})
	} else {
		checks = append(checks, adapter.DiagnosticCheck{
			Name:   "data-dir",
			Status: "ok",
			Detail: dir,
		})
	}

	if a.Dir == "" {
		registry := filepath.Join(DefaultDir(), "projects.json")

		dbs := loadProjectDBs()
		if len(dbs) == 0 {
			if _, err := os.Stat(registry); err != nil {
				checks = append(checks, adapter.DiagnosticCheck{
					Name:   "projects.json",
					Status: "warn",
					Detail: "registry not found at " + registry,
				})
			} else {
				checks = append(checks, adapter.DiagnosticCheck{
					Name:   "projects.json",
					Status: "warn",
					Detail: fmt.Sprintf("registry at %s contains no valid project databases", registry),
				})
			}
		} else {
			checks = append(checks, adapter.DiagnosticCheck{
				Name:   "projects.json",
				Status: "ok",
				Detail: fmt.Sprintf("%d project database(s) discovered", len(dbs)),
			})
		}
	}

	for _, dbPath := range a.enumerateDBPaths() {
		label := dbLabel(dbPath)

		h, err := openAt(dbPath)
		if err != nil {
			checks = append(checks, adapter.DiagnosticCheck{
				Name:   "db:" + label,
				Status: "error",
				Detail: fmt.Sprintf("cannot open: %v", err),
			})

			continue
		}

		if h == nil {
			checks = append(checks, adapter.DiagnosticCheck{
				Name:   "db:" + label,
				Status: "warn",
				Detail: "database file not found or empty",
			})

			continue
		}

		missing := h.missingColumns()
		_ = h.close()

		if len(missing) > 0 {
			checks = append(checks, adapter.DiagnosticCheck{
				Name:   "db:" + label,
				Status: "warn",
				Detail: "missing columns: " + strings.Join(missing, ", "),
			})
		} else {
			checks = append(checks, adapter.DiagnosticCheck{
				Name:   "db:" + label,
				Status: "ok",
				Detail: "schema current",
			})
		}
	}

	return checks
}

// dbLabel produces a short label for a database path, using the parent
// directory name so multiple project databases are distinguishable in
// the doctor output.
func dbLabel(dbPath string) string {
	parent := filepath.Dir(dbPath)

	base := filepath.Base(parent)
	if base == "." || base == string(filepath.Separator) {
		return filepath.Base(dbPath)
	}

	return base
}
