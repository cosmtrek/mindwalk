package crush

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	_ "modernc.org/sqlite"
)

func findCheck(checks []adapter.DiagnosticCheck, prefix string) (adapter.DiagnosticCheck, bool) {
	for _, c := range checks {
		if strings.HasPrefix(c.Name, prefix) {
			return c, true
		}
	}

	return adapter.DiagnosticCheck{}, false
}

func TestDiagnosticsFullSchema(t *testing.T) {
	root := t.TempDir()

	data := filepath.Join(root, ".crush")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(data, "crush.db")

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}

	db.SetMaxOpenConns(1)
	// Full schema with every column schemaMissingColumns expects in messages.
	_, err = db.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY, parent_session_id TEXT, title TEXT NOT NULL,
		message_count INTEGER NOT NULL DEFAULT 0,
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		cost REAL NOT NULL DEFAULT 0.0,
		updated_at INTEGER NOT NULL, created_at INTEGER NOT NULL, todos TEXT
	);
	CREATE TABLE messages (
		id TEXT PRIMARY KEY, session_id TEXT NOT NULL, role TEXT NOT NULL,
		parts TEXT NOT NULL DEFAULT '[]',
		model TEXT, provider TEXT, parent_session_id TEXT,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, finished_at INTEGER
	);`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO sessions (id, title, updated_at, created_at) VALUES ('s1', 'test', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}

	_ = db.Close()

	a := Adapter{Dir: data}
	checks := a.Diagnostics()

	dirCheck, ok := findCheck(checks, "data-dir")
	if !ok {
		t.Fatal("expected a data-dir check")
	}

	if dirCheck.Status != "ok" {
		t.Fatalf("data-dir status = %q, want ok", dirCheck.Status)
	}

	dbCheck, ok := findCheck(checks, "db:")
	if !ok {
		t.Fatal("expected a db:* check")
	}

	if dbCheck.Status != "ok" {
		t.Fatalf("db status = %q, want ok (detail: %s)", dbCheck.Status, dbCheck.Detail)
	}

	if !strings.Contains(dbCheck.Detail, "schema current") {
		t.Fatalf("db detail = %q, want 'schema current'", dbCheck.Detail)
	}
}

func TestDiagnosticsMissingColumns(t *testing.T) {
	root := t.TempDir()

	data := filepath.Join(root, ".crush")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(data, "crush.db")

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}

	db.SetMaxOpenConns(1)

	_, err = db.Exec(`CREATE TABLE sessions (id TEXT, title TEXT, updated_at INTEGER, created_at INTEGER);
		CREATE TABLE messages (id TEXT, session_id TEXT, role TEXT, parts TEXT, created_at INTEGER, updated_at INTEGER);`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO sessions (id, title, updated_at, created_at) VALUES ('s1', 'test', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}

	_ = db.Close()

	a := Adapter{Dir: data}
	checks := a.Diagnostics()

	dbCheck, ok := findCheck(checks, "db:")
	if !ok {
		t.Fatal("expected a db:* check")
	}

	if dbCheck.Status != "warn" {
		t.Fatalf("db status = %q, want warn", dbCheck.Status)
	}

	if !strings.Contains(dbCheck.Detail, "missing columns") {
		t.Fatalf("db detail = %q, want 'missing columns'", dbCheck.Detail)
	}
}

func TestDiagnosticsDataDirMissing(t *testing.T) {
	a := Adapter{Dir: filepath.Join(t.TempDir(), "nonexistent")}
	checks := a.Diagnostics()

	dirCheck, ok := findCheck(checks, "data-dir")
	if !ok {
		t.Fatal("expected a data-dir check")
	}

	if dirCheck.Status != "warn" {
		t.Fatalf("data-dir status = %q, want warn", dirCheck.Status)
	}
}
