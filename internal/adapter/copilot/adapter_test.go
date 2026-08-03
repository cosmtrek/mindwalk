package copilot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSession(t *testing.T) {
	trace, err := (Adapter{}).Parse(filepath.Join("..", "..", "..", "testdata", "copilot-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if trace.Session.ID != "copilot-demo" {
		t.Fatalf("session id = %q", trace.Session.ID)
	}
	if trace.Session.Model != "copilot-cli" {
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
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","cwd":`+quote(root)+`,"sessionId":"noise","message":{"role":"assistant","content":[{"type":"tool_call","id":"bash","name":"Bash","input":{"command":"printf 'please check the main.go file\nupdated to v1.5\n+ cs.fontSize = 27.2px\nsrc/main.go\n'"}}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:02Z","cwd":`+quote(root)+`,"sessionId":"noise","message":{"role":"user","content":[{"tool_call_id":"bash","type":"tool_result","content":"please check the main.go file\nupdated to v1.5\n+ cs.fontSize = 27.2px\nsrc/main.go\n","is_error":false}]}}`,
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
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","cwd":"/tmp","sessionId":"pending","message":{"role":"assistant","content":[{"type":"tool_call","id":"a","name":"Read","input":{"file_path":"/tmp/a.go"}},{"type":"tool_call","id":"b","name":"Read","input":{"file_path":"/tmp/b.go"}},{"type":"tool_call","id":"c","name":"Read","input":{"file_path":"/tmp/c.go"}}]}}`,
	)
	for i := 0; i < 20; i++ {
		trace, err := (Adapter{}).Parse(session)
		if err != nil {
			t.Fatal(err)
		}
		if len(trace.Events) != 3 {
			t.Fatalf("iter %d: events = %d", i, len(trace.Events))
		}
	}
}

func TestParseCopilotSessionKey(t *testing.T) {
	key := SessionKey("copilot-cli", "/home/user/.copilot/sessions/abc123.jsonl")
	if key != "copilot-cli|/home/user/.copilot/sessions/abc123.jsonl" {
		t.Fatalf("key = %q", key)
	}
}

func writeSession(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		_, err := f.WriteString(l + "\n")
		if err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
}

func quote(s string) string {
	return "\"" + s + "\""
}