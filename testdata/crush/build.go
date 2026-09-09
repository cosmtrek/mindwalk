//go:build ignore

// This is a standalone fixture-builder tool for testdata/crush/crush.db.
// Run with: go run testdata/crush/build.go
//
// It recreates the committed fixture from scratch so the test database
// is no longer a black box. The schema and seed data mirror what a real
// Crush session looks like: root session, sub-agent child session,
// read/write/bash tool calls, token economics, and a read_files table.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func main() {
	dir := "."
	dbPath := filepath.Join(dir, "crush.db")
	for _, suffix := range []string{"", "-shm", "-wal"} {
		_ = os.Remove(dbPath + suffix)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		panic(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	schema := `
		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			parent_session_id TEXT,
			title TEXT NOT NULL,
			message_count INTEGER NOT NULL DEFAULT 0,
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			cost REAL NOT NULL DEFAULT 0.0,
			updated_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			todos TEXT
		);
		CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			parts TEXT NOT NULL DEFAULT '[]',
			model TEXT,
			provider TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			finished_at INTEGER
		);
		CREATE TABLE IF NOT EXISTS read_files (
			session_id TEXT NOT NULL,
			path TEXT NOT NULL,
			read_at INTEGER NOT NULL
		);
	`
	if _, err := db.Exec(schema); err != nil {
		panic(err)
	}

	// Timestamps in Unix SECONDS (the adapter treats them as seconds per 3f547fc).
	const base = 1785585600 // 2026-08-04T~12:00 UTC

	// Root session with token economics.
	_, err = db.Exec(
		`INSERT INTO sessions (id, parent_session_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		 VALUES ('fixture-root', NULL, 'Fixture root session', 10, 5000, 1500, 0.0234, ?, ?)`,
		base+9, base,
	)
	if err != nil {
		panic(err)
	}

	// Child (sub-agent) session.
	_, err = db.Exec(
		`INSERT INTO sessions (id, parent_session_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		 VALUES ('m_assistant_1$$call_agent_1', 'fixture-root', 'Agent: read server', 1, 0, 0, 0.0, ?, ?)`,
		base+3, base+1,
	)
	if err != nil {
		panic(err)
	}

	messages := []struct {
		id, sessionID, role, parts, model string
		createdAt                         int64
	}{
		{
			"m_user", "fixture-root", "user",
			`[{"data":{"text":"Read internal/server/server.go."},"type":"text"},{"data":{"reason":"stop","time":0},"type":"finish"}]`,
			"", base,
		},
		{
			"m_assistant_1", "fixture-root", "assistant",
			`[{"data":{"text":"Reading the server."},"type":"text"},{"data":{"finished":true,"id":"call_agent_1","input":"{\"task_title\":\"read server\",\"agent_type\":\"explore\",\"message\":\"look at internal/server/server.go\"}","name":"agent","provider_executed":false},"type":"tool_call"}]`,
			"minimax/minimax-m3", base + 1,
		},
		{
			"m_tool_agent", "fixture-root", "tool",
			`[{"data":{"content":"{\"agent_id\":\"m_assistant_1\",\"nickname\":\"explore\",\"task_name\":\"read server\"}","is_error":false,"name":"agent","tool_call_id":"call_agent_1"},"type":"tool_result"}]`,
			"", base + 2,
		},
		{
			"m_assistant_2", "fixture-root", "assistant",
			`[{"data":{"text":"Let me read the main file."},"type":"text"},{"data":{"finished":true,"id":"call_read_1","input":"{\"file_path\":\"/home/lars/forks/mindwalk/cmd/mindwalk/main.go\"}","name":"read","provider_executed":false},"type":"tool_call"}]`,
			"minimax/minimax-m3", base + 3,
		},
		{
			"m_tool_read", "fixture-root", "tool",
			`[{"data":{"content":"package main\n\nfunc main() {}\n","is_error":false,"name":"read","tool_call_id":"call_read_1"},"type":"tool_result"}]`,
			"", base + 4,
		},
		{
			"m_assistant_3", "fixture-root", "assistant",
			`[{"data":{"text":"Now I'll edit the server."},"type":"text"},{"data":{"finished":true,"id":"call_write_1","input":"{\"file_path\":\"/home/lars/forks/mindwalk/internal/server/server.go\"}","name":"write","provider_executed":false},"type":"tool_call"}]`,
			"minimax/minimax-m3", base + 5,
		},
		{
			"m_tool_write", "fixture-root", "tool",
			`[{"data":{"content":"File written successfully.","is_error":false,"name":"write","tool_call_id":"call_write_1"},"type":"tool_result"}]`,
			"", base + 6,
		},
		{
			"m_assistant_4", "fixture-root", "assistant",
			`[{"data":{"text":"Running tests."},"type":"text"},{"data":{"finished":true,"id":"call_bash_1","input":"{\"command\":\"go test ./internal/server/\"}","name":"bash","provider_executed":false},"type":"tool_call"}]`,
			"minimax/minimax-m3", base + 7,
		},
		{
			"m_tool_bash", "fixture-root", "tool",
			`[{"data":{"content":"ok\tgithub.com/cosmtrek/mindwalk/internal/server\t0.3s\n","is_error":false,"name":"bash","tool_call_id":"call_bash_1"},"type":"tool_result"}]`,
			"", base + 8,
		},
		{
			"m_user_2", "fixture-root", "user",
			`[{"data":{"text":"Looks good!"},"type":"text"},{"data":{"reason":"stop","time":0},"type":"finish"}]`,
			"", base + 9,
		},
		{
			"m_child_1", "m_assistant_1$$call_agent_1", "assistant",
			`[{"data":{"text":"Reading the file."},"type":"text"}]`,
			"minimax/minimax-m3", base + 2,
		},
	}

	for _, m := range messages {
		var model any
		if m.model == "" {
			model = nil
		} else {
			model = m.model
		}
		_, err = db.Exec(
			`INSERT INTO messages (id, session_id, role, parts, model, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.id, m.sessionID, m.role, m.parts, model, m.createdAt, m.createdAt,
		)
		if err != nil {
			panic(fmt.Errorf("insert message %s: %w", m.id, err))
		}
	}

	// read_files table for exact-read observability.
	_, err = db.Exec(
		`INSERT INTO read_files (session_id, path, read_at) VALUES ('fixture-root', '/home/lars/forks/mindwalk/cmd/mindwalk/main.go', ?)`,
		1700000000,
	)
	if err != nil {
		panic(err)
	}

	fmt.Println("Fixture written to", dbPath)
}
