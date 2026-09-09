package crush

import (
	"database/sql"
	"fmt"
	"testing"
)

// TestListSessionsLargeDatabase verifies the adapter handles a database
// with many sessions without errors. This is a smoke test for scale —
// it does not assert performance, only correctness under volume.
func TestListSessionsLargeDatabase(t *testing.T) {
	const sessionCount = 500

	dir := t.TempDir()
	dbPath := dir + "/crush.db"
	db := createCrushDBAt(t, dbPath, func(db *sql.DB) {
		for i := range sessionCount {
			id := fmt.Sprintf("sess-large-%04d", i)

			_, err := db.Exec(
				`INSERT INTO sessions (id, title, message_count, updated_at, created_at) VALUES (?, ?, ?, ?, ?)`,
				id, fmt.Sprintf("Session %d", i), 0, 1785585600+i, 1785585600+i,
			)
			if err != nil {
				t.Fatal(err)
			}
		}
	})
	_ = db

	a := NewAdapter(dir)

	metas, err := a.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions on %d sessions: %v", sessionCount, err)
	}

	if len(metas) != sessionCount {
		t.Fatalf("got %d sessions, want %d", len(metas), sessionCount)
	}
}

// TestParseLargeMessageHistory verifies the adapter parses a session
// with many messages without errors or panics.
func TestParseLargeMessageHistory(t *testing.T) {
	const messageCount = 1000

	dir := t.TempDir()
	dbPath := dir + "/crush.db"
	createCrushDBAt(t, dbPath, func(db *sql.DB) {
		_, err := db.Exec(
			`INSERT INTO sessions (id, title, message_count, updated_at, created_at) VALUES ('sess-many-msgs', 'Many messages', ?, ?, ?)`,
			messageCount,
			1785585600+messageCount,
			1785585600,
		)
		if err != nil {
			t.Fatal(err)
		}

		for i := range messageCount {
			msgID := fmt.Sprintf("msg-%04d", i)

			role := "assistant"
			if i%2 == 0 {
				role = "tool"
			}

			var parts string

			if role == "assistant" {
				callID := fmt.Sprintf("call_%04d", i)
				parts = fmt.Sprintf(
					`[{"data":{"text":"step %d"},"type":"text"},{"data":{"finished":true,"id":"%s","input":"{\"file_path\":\"/repo/file_%d.go\"}","name":"read"},"type":"tool_call"}]`,
					i,
					callID,
					i,
				)
			} else {
				parts = fmt.Sprintf(
					`[{"data":{"content":"ok","is_error":false,"name":"read","tool_call_id":"call_%04d"},"type":"tool_result"}]`,
					i-1,
				)
			}

			ts := 1785585600 + i

			_, err := db.Exec(
				`INSERT INTO messages (id, session_id, role, parts, model, created_at, updated_at) VALUES (?, 'sess-many-msgs', ?, ?, 'test-model', ?, ?)`,
				msgID,
				role,
				parts,
				ts,
				ts,
			)
			if err != nil {
				t.Fatal(err)
			}
		}
	})

	a := NewAdapter(dir)

	trace, err := a.Parse(SessionPath("sess-many-msgs"))
	if err != nil {
		t.Fatalf("Parse on %d messages: %v", messageCount, err)
	}

	if trace.Session.EventCount == 0 {
		t.Fatal("EventCount = 0, want > 0")
	}
}

// BenchmarkListSessionsLargeDB measures ListSessions performance with
// a large session count. Run with: go test -bench=BenchmarkListSessionsLargeDB.
func BenchmarkListSessionsLargeDB(b *testing.B) {
	const sessionCount = 2000

	dir := b.TempDir()
	dbPath := dir + "/crush.db"

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		b.Fatal(err)
	}

	defer func() { _ = db.Close() }()

	db.SetMaxOpenConns(1)

	schema := `
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY, parent_session_id TEXT, title TEXT NOT NULL,
			message_count INTEGER NOT NULL DEFAULT 0, prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0, cost REAL NOT NULL DEFAULT 0.0,
			updated_at INTEGER NOT NULL, created_at INTEGER NOT NULL, todos TEXT
		);
		CREATE TABLE messages (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, role TEXT NOT NULL,
			parts TEXT NOT NULL DEFAULT '[]', model TEXT, provider TEXT,
			created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, finished_at INTEGER
		);
	`
	if _, err := db.Exec(schema); err != nil {
		b.Fatal(err)
	}

	for i := range sessionCount {
		_, err := db.Exec(
			`INSERT INTO sessions (id, title, message_count, updated_at, created_at) VALUES (?, ?, 0, ?, ?)`,
			fmt.Sprintf("bench-sess-%04d", i), "bench", 1785585600+i, 1785585600+i,
		)
		if err != nil {
			b.Fatal(err)
		}
	}

	a := NewAdapter(dir)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := a.ListSessions(); err != nil {
			b.Fatal(err)
		}
	}
}
