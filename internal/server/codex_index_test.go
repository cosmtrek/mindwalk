package server

import (
	"os"
	"path/filepath"
	"testing"
)

// A Codex thread rename only touches session_index.jsonl, never the session
// file. The summary cache must treat the index as a summary input, or the
// rename stays invisible for the life of the serve process.
func TestFreshScanPicksUpCodexTitleIndexChange(t *testing.T) {
	base := t.TempDir()
	codexDir := filepath.Join(base, "sessions")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeServerJSONL(t, filepath.Join(codexDir, "s1.jsonl"), map[string]any{
		"timestamp": "2026-07-09T00:00:00Z",
		"type":      "session_meta",
		"payload":   map[string]any{"id": "codex-title", "cwd": "/tmp"},
	})
	writeIndex := func(title string) {
		writeServerJSONL(t, filepath.Join(base, "session_index.jsonl"),
			map[string]any{"id": "codex-title", "thread_name": title})
	}
	writeIndex("Title One")

	s := New(Config{ClaudeDir: filepath.Join(t.TempDir(), "claude"), CodexDir: codexDir, PiDir: filepath.Join(t.TempDir(), "pi")})
	initial := requestSessions(t, s, "/api/sessions")
	if len(initial) != 1 || initial[0].Title != "Title One" {
		t.Fatalf("initial sessions = %#v", initial)
	}

	writeIndex("Title Two Updated")
	fresh := requestSessions(t, s, "/api/sessions?fresh=1")
	if len(fresh) != 1 || fresh[0].Title != "Title Two Updated" {
		t.Fatalf("index change not picked up: %#v", fresh)
	}
}
