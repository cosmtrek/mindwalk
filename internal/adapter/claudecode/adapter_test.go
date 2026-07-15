package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSession(t *testing.T) {
	trace, err := (Adapter{}).Parse(filepath.Join("..", "..", "..", "testdata", "claude-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if trace.Session.ID != "demo-session" {
		t.Fatalf("session id = %q", trace.Session.ID)
	}
	if trace.Session.Title != "Demo trace" {
		t.Fatalf("title = %q", trace.Session.Title)
	}
	if trace.Session.Model != "claude-test" {
		t.Fatalf("model = %q", trace.Session.Model)
	}
	if len(trace.Events) != 2 {
		t.Fatalf("events = %d", len(trace.Events))
	}
	read := trace.Events[0]
	if read.Action != "read" || len(read.Targets) != 1 || read.Targets[0].Path != "src/main.go" {
		t.Fatalf("bad read event: %#v", read)
	}
	if got := read.Targets[0].Lines[0]; got != [2]int{10, 14} {
		t.Fatalf("line range = %#v", got)
	}
	edit := trace.Events[1]
	if edit.Action != "edit" || edit.Targets[0].Touch != "edit" {
		t.Fatalf("bad edit event: %#v", edit)
	}
	if trace.Stats.Edited != 1 || trace.Stats.Fovea != 1 {
		t.Fatalf("stats = %#v", trace.Stats)
	}
}

func TestParseSkipsBashNoiseAndKeepsExistingWeakPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(t.TempDir(), "session.jsonl")
	writeSession(t, session,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","cwd":`+quote(root)+`,"sessionId":"noise","message":{"role":"user","content":"x"}}`,
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","cwd":`+quote(root)+`,"sessionId":"noise","message":{"role":"assistant","content":[{"type":"tool_use","id":"bash","name":"Bash","input":{"command":"printf 'please check the main.go file\nupdated to v1.5\n+ cs.fontSize = 27.2px\nsrc/main.go\n'"}}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:02Z","cwd":`+quote(root)+`,"sessionId":"noise","message":{"role":"user","content":[{"tool_use_id":"bash","type":"tool_result","content":"please check the main.go file\nupdated to v1.5\n+ cs.fontSize = 27.2px\nsrc/main.go\n","is_error":false}]}}`,
	)
	trace, err := (Adapter{}).Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Events) != 1 {
		t.Fatalf("events = %d", len(trace.Events))
	}
	targets := trace.Events[0].Targets
	if len(targets) != 1 {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[0].Path != "src/main.go" || !targets[0].Weak {
		t.Fatalf("target = %#v", targets[0])
	}
}

func TestParsePendingToolUsesRemainDeterministic(t *testing.T) {
	session := filepath.Join(t.TempDir(), "pending.jsonl")
	writeSession(t, session,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","cwd":"/tmp","sessionId":"pending","message":{"role":"user","content":"x"}}`,
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","cwd":"/tmp","sessionId":"pending","message":{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Read","input":{"file_path":"/tmp/a.go"}},{"type":"tool_use","id":"b","name":"Read","input":{"file_path":"/tmp/b.go"}},{"type":"tool_use","id":"c","name":"Read","input":{"file_path":"/tmp/c.go"}}]}}`,
	)
	for i := 0; i < 20; i++ {
		trace, err := (Adapter{}).Parse(session)
		if err != nil {
			t.Fatal(err)
		}
		got := []string{trace.Events[0].Targets[0].Path, trace.Events[1].Targets[0].Path, trace.Events[2].Targets[0].Path}
		want := []string{"a.go", "b.go", "c.go"}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("run %d order = %#v", i, got)
			}
		}
	}
}

func TestCompactionUsesSubtypeOnly(t *testing.T) {
	session := filepath.Join(t.TempDir(), "compact.jsonl")
	writeSession(t, session,
		`{"type":"system","subtype":"local_command","content":"compact but not a boundary","timestamp":"2026-07-09T00:00:00Z","sessionId":"compact"}`,
		`{"type":"system","subtype":"compact_boundary","content":"","timestamp":"2026-07-09T00:00:01Z","sessionId":"compact"}`,
	)
	trace, err := (Adapter{}).Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Marks) != 1 || trace.Marks[0].Type != "compaction" {
		t.Fatalf("marks = %#v", trace.Marks)
	}
}

func TestUserMessageMarkCarriesNote(t *testing.T) {
	long := strings.Repeat("字", 2100)
	session := filepath.Join(t.TempDir(), "notes.jsonl")
	writeSession(t, session,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","cwd":"/tmp","sessionId":"notes","message":{"role":"user","content":"fix the login bug"}}`,
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","cwd":"/tmp","sessionId":"notes","message":{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Read","input":{"file_path":"/tmp/a.go"}}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:02Z","cwd":"/tmp","sessionId":"notes","message":{"role":"user","content":[{"tool_use_id":"a","type":"tool_result","content":"ok","is_error":false}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:03Z","cwd":"/tmp","sessionId":"notes","message":{"role":"user","content":"`+long+`"}}`,
	)
	trace, err := (Adapter{}).Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Marks) != 2 {
		t.Fatalf("marks = %#v", trace.Marks)
	}
	if trace.Marks[0].Note != "fix the login bug" {
		t.Fatalf("note = %q", trace.Marks[0].Note)
	}
	// The ellipsis fits inside the 2000-rune budget the schema promises.
	truncated := []rune(trace.Marks[1].Note)
	if len(truncated) != 2000 || truncated[len(truncated)-1] != '…' {
		t.Fatalf("truncated note runes = %d, last = %q", len(truncated), truncated[len(truncated)-1])
	}
	// Summarize counts the same turns Parse marks — tool results and other
	// non-message user lines stay out of both.
	meta, err := (Adapter{}).Summarize(session)
	if err != nil {
		t.Fatal(err)
	}
	if meta.UserTurns != 2 {
		t.Fatalf("summarized userTurns = %d, want 2", meta.UserTurns)
	}
}

func TestSummarizeRecognizesUnknownClaudeLineWithSessionID(t *testing.T) {
	session := filepath.Join(t.TempDir(), "metadata.jsonl")
	writeSession(t, session,
		`{"type":"metadata","timestamp":"2026-07-09T00:00:00Z","sessionId":"metadata-session","cwd":"/tmp"}`,
	)
	meta, err := (Adapter{}).Summarize(session)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != "metadata-session" || meta.Cwd != "/tmp" {
		t.Fatalf("meta = %#v", meta)
	}
}

func TestSummarizePopulatesSidechainMetadata(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), "root-session", "subagents")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(rootDir, "agent-child.jsonl")
	writeSession(t, session,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"root-session","agentId":"child","isSidechain":true,"cwd":"/tmp","message":{"role":"user","content":"hello"}}`,
	)
	if err := os.WriteFile(strings.TrimSuffix(session, ".jsonl")+".meta.json", []byte(`{"agentType":"Explore","description":"Inspect child","toolUseId":"call-child","spawnDepth":2}`), 0o644); err != nil {
		t.Fatal(err)
	}

	meta, err := (Adapter{}).Summarize(session)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != "child" {
		t.Fatalf("id = %q, want child", meta.ID)
	}
	if !meta.Auxiliary || meta.Agent == nil {
		t.Fatalf("meta = %#v", meta)
	}
	if meta.Agent.SourceID != "child" || meta.Agent.RootSessionID != "root-session" || meta.Agent.Depth != 2 ||
		meta.Agent.Role != "Explore" || meta.Agent.LaunchCallID != "call-child" {
		t.Fatalf("agent meta = %#v", *meta.Agent)
	}
}

func TestSummarizeHandlesLargeJSONLines(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "large.jsonl")
	largeText := strings.Repeat("x", 21*1024*1024)
	writeSession(t, session,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","cwd":"/tmp","sessionId":"large","message":{"role":"user","content":"`+largeText+`"}}`,
	)

	adapter := Adapter{Dir: dir}
	meta, err := adapter.Summarize(session)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != "large" {
		t.Fatalf("id = %q", meta.ID)
	}
	sessions, err := adapter.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "large" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func writeSession(t *testing.T, path string, lines ...string) {
	t.Helper()
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func quote(s string) string {
	return `"` + filepath.ToSlash(s) + `"`
}
