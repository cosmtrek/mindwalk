package antigravity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAntigravitySession(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	logDir := filepath.Join(dir, "sess-123", ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(logDir, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseAntigravitySession(t *testing.T) {
	root := t.TempDir()
	mainGo := filepath.Join(root, "src", "main.go")

	path := writeAntigravitySession(t,
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-22T10:00:00Z","content":"<USER_REQUEST>\nfix the bug in main.go\n</USER_REQUEST>"}`,
		fmt.Sprintf(`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-22T10:00:05Z","tool_calls":[{"name":"view_file","args":{"AbsolutePath":%q,"StartLine":1,"EndLine":50},"toolAction":"Viewing file","toolSummary":"View main.go"}]}`, mainGo),
		`{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-09-22T10:00:08Z","content":"package main\n\nfunc main() {}"}`,
		fmt.Sprintf(`{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-22T10:00:10Z","tool_calls":[{"name":"replace_file_content","args":{"TargetFile":%q,"StartLine":3,"EndLine":3,"TargetContent":"func main() {}","ReplacementContent":"func main() { println(\"fixed\") }"},"toolAction":"Editing file","toolSummary":"Fix bug"}]}`, mainGo),
		`{"step_index":4,"source":"MODEL","type":"GENERIC","status":"ERROR","error":"Failed to replace content","created_at":"2026-09-22T10:00:15Z","content":""}`,
		`{"step_index":5,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-22T10:00:20Z","content":"I attempted the fix but encountered an error."}`,
	)

	trace, err := Adapter{}.Parse(path)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if trace.Session.Harness != "antigravity" {
		t.Fatalf("expected harness antigravity, got %q", trace.Session.Harness)
	}
	if trace.Session.ID != "sess-123" {
		t.Fatalf("expected session ID sess-123, got %q", trace.Session.ID)
	}
	if trace.Session.Title != "fix the bug in main.go" {
		t.Fatalf("expected title 'fix the bug in main.go', got %q", trace.Session.Title)
	}
	if len(trace.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(trace.Events))
	}

	// Verify Read event
	readEvent := trace.Events[0]
	if readEvent.Tool != "view_file" || readEvent.Action != "read" || readEvent.IsError {
		t.Fatalf("unexpected read event: %#v", readEvent)
	}
	if len(readEvent.Targets) != 1 || !strings.HasSuffix(readEvent.Targets[0].Path, "main.go") {
		t.Fatalf("unexpected read targets: %#v", readEvent.Targets)
	}

	// Verify Edit event
	editEvent := trace.Events[1]
	if editEvent.Tool != "replace_file_content" || editEvent.Action != "edit" || !editEvent.IsError {
		t.Fatalf("unexpected edit event: %#v", editEvent)
	}

	// Verify Marks
	if len(trace.Marks) != 1 || trace.Marks[0].Type != "user-message" || trace.Marks[0].Note != "fix the bug in main.go" {
		t.Fatalf("unexpected marks: %#v", trace.Marks)
	}

	// Verify Stats
	if trace.Stats.UserTurns != 1 || trace.Stats.Errors.Edit != 1 {
		t.Fatalf("unexpected stats: %#v", trace.Stats)
	}
}

func TestSummarizeAntigravitySession(t *testing.T) {
	root := t.TempDir()
	mainGo := filepath.Join(root, "src", "main.go")

	path := writeAntigravitySession(t,
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-22T10:00:00Z","content":"<USER_REQUEST>\nexplore open-source projects\n</USER_REQUEST>"}`,
		fmt.Sprintf(`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-22T10:00:05Z","tool_calls":[{"name":"view_file","args":{"AbsolutePath":%q},"toolAction":"Viewing file","toolSummary":"View main.go"}]}`, mainGo),
		`{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-09-22T10:00:08Z","content":"ok"}`,
	)

	meta, err := Adapter{}.Summarize(path)
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}

	if meta.Harness != "antigravity" || meta.ID != "sess-123" {
		t.Fatalf("unexpected meta: %#v", meta)
	}
	if meta.Title != "explore open-source projects" {
		t.Fatalf("unexpected title: %q", meta.Title)
	}
	if meta.UserTurns != 1 {
		t.Fatalf("expected 1 user turn, got %d", meta.UserTurns)
	}
	if meta.EventCount != 1 {
		t.Fatalf("expected 1 event, got %d", meta.EventCount)
	}
}

func TestLiveAntigravitySessions(t *testing.T) {
	adapter := Adapter{}
	dir := DefaultDir()
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Skip("Default Antigravity brain dir does not exist")
	}

	metas, err := adapter.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	t.Logf("Discovered %d Antigravity sessions", len(metas))
	for i, m := range metas {
		t.Logf("Session [%d]: ID=%s Title=%q Events=%d UserTurns=%d Started=%s", i, m.ID, m.Title, m.EventCount, m.UserTurns, m.StartedAt)
		if i < 3 {
			trace, err := adapter.Parse(m.Path)
			if err != nil {
				t.Fatalf("Failed to parse live session %s: %v", m.Path, err)
			}
			t.Logf("  Parsed trace: Events=%d Marks=%d Errors=%d Observability=%s", len(trace.Events), len(trace.Marks), trace.Stats.Errors.Edit+trace.Stats.Errors.Exec, trace.Stats.Observability.Errors)
		}
	}
}

func TestWorkspaceURICrossPlatform(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"file:///C:/Users/Pratikk/repo", filepath.Clean("C:/Users/Pratikk/repo")},
		{"file:///home/user/project", filepath.Clean("/home/user/project")},
		{"file:///Users/dev/workspace", filepath.Clean("/Users/dev/workspace")},
		{"file://localhost/tmp/code", filepath.Clean("/tmp/code")},
		{"", ""},
		{"not-a-uri", ""},
	}

	for _, c := range cases {
		got := parseWorkspaceURI(c.input)
		if c.expected != "" && !strings.HasSuffix(got, c.expected) && got != c.expected {
			t.Errorf("parseWorkspaceURI(%q) = %q, want suffix or match %q", c.input, got, c.expected)
		}
	}
}

func TestChaosCorruptAndEdgeCases(t *testing.T) {
	cases := []struct {
		name       string
		lines      []string
		wantEvents int
		wantErr    bool
	}{
		{
			name: "mid-write crash / truncated JSON",
			lines: []string{
				`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>crash test</USER_REQUEST>"}`,
				`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/file.txt"}}]}`,
				`{"step_index":2,"source":"MODEL","type":"GENERIC"`, // truncated mid-line
			},
			wantEvents: 1, // The pending tool call should be salvaged as outcome-unknown
			wantErr:    false,
		},
		{
			name: "empty steps and whitespace lines",
			lines: []string{
				"",
				"   ",
				`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"hi"}`,
				"",
				`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","content":"just talking, no tools"}`,
				"   \t\r\n",
			},
			wantEvents: 0,
			wantErr:    false,
		},
		{
			name: "unclosed XML user request with metadata tags",
			lines: []string{
				`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>\nUnclosed request\n<ADDITIONAL_METADATA>\nignore me\n</ADDITIONAL_METADATA>"}`,
				`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","tool_calls":[{"name":"run_command","args":{"CommandLine":"dir"}}]}`,
				`{"step_index":2,"source":"MODEL","type":"GENERIC","content":"volume C"}`,
			},
			wantEvents: 1,
			wantErr:    false,
		},
		{
			name: "pure metadata injection dropped",
			lines: []string{
				`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_SETTINGS_CHANGE>\nmodel changed\n</USER_SETTINGS_CHANGE>"}`,
				`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","tool_calls":[{"name":"run_command","args":{"CommandLine":"echo 1"}}]}`,
				`{"step_index":2,"source":"MODEL","type":"GENERIC","content":"1"}`,
			},
			wantEvents: 1,
			wantErr:    false,
		},
		{
			name: "checkpoint mark parsing",
			lines: []string{
				`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"checkpoint test"}`,
				`{"step_index":1,"source":"SYSTEM","type":"CHECKPOINT"}`,
			},
			wantEvents: 0,
			wantErr:    false,
		},
		{
			name: "completely random non-antigravity json",
			lines: []string{
				`{"foo":"bar","baz":123}`,
				`{"items":[1,2,3]}`,
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeAntigravitySession(t, tc.lines...)
			trace, err := Adapter{}.Parse(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Parse error = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil && len(trace.Events) != tc.wantEvents {
				t.Fatalf("Events count = %d, want %d", len(trace.Events), tc.wantEvents)
			}
		})
	}
}

func BenchmarkParse(b *testing.B) {
	dir := b.TempDir()
	logPath := filepath.Join(dir, "transcript.jsonl")

	// Generate a realistic 1,000-event session stream
	var builder strings.Builder
	builder.WriteString(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-22T10:00:00Z","content":"<USER_REQUEST>benchmark run</USER_REQUEST>"}` + "\n")
	for i := 1; i <= 500; i++ {
		builder.WriteString(fmt.Sprintf(`{"step_index":%d,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-22T10:01:%02dZ","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"C:\\repo\\file_%d.go","StartLine":1,"EndLine":100}}]}`+"\n", i*2-1, i%60, i))
		builder.WriteString(fmt.Sprintf(`{"step_index":%d,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-09-22T10:01:%02dZ","content":"file contents line 1-100"}`+"\n", i*2, i%60))
	}
	if err := os.WriteFile(logPath, []byte(builder.String()), 0o644); err != nil {
		b.Fatal(err)
	}

	adapter := Adapter{}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		trace, err := adapter.Parse(logPath)
		if err != nil || len(trace.Events) != 500 {
			b.Fatalf("Parse benchmark failed: err=%v, events=%d", err, len(trace.Events))
		}
	}
}

func BenchmarkSummarize(b *testing.B) {
	dir := b.TempDir()
	logPath := filepath.Join(dir, "transcript.jsonl")

	var builder strings.Builder
	builder.WriteString(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-22T10:00:00Z","content":"<USER_REQUEST>benchmark summary</USER_REQUEST>"}` + "\n")
	for i := 1; i <= 500; i++ {
		builder.WriteString(fmt.Sprintf(`{"step_index":%d,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-22T10:01:%02dZ","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"C:\\repo\\file_%d.go"}}]}`+"\n", i*2-1, i%60, i))
		builder.WriteString(fmt.Sprintf(`{"step_index":%d,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-09-22T10:01:%02dZ","content":"file contents line 1-100"}`+"\n", i*2, i%60))
	}
	if err := os.WriteFile(logPath, []byte(builder.String()), 0o644); err != nil {
		b.Fatal(err)
	}

	adapter := Adapter{}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		meta, err := adapter.Summarize(logPath)
		if err != nil || meta.EventCount != 500 {
			b.Fatalf("Summarize benchmark failed: err=%v, events=%d", err, meta.EventCount)
		}
	}
}

func FuzzParseTrace(f *testing.F) {
	// Seed corpus with valid and semi-valid Antigravity lines
	f.Add([]byte(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"<USER_REQUEST>test</USER_REQUEST>"}`))
	f.Add([]byte(`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/test.go"}}]}`))
	f.Add([]byte(`{"step_index":2,"source":"MODEL","type":"GENERIC","content":"data"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte("\x00\x01\x02\xff\xfe"))

	adapter := Adapter{}

	f.Fuzz(func(t *testing.T, data []byte) {
		tmpDir := t.TempDir()
		sessionFile := filepath.Join(tmpDir, "transcript.jsonl")
		if err := os.WriteFile(sessionFile, data, 0o644); err != nil {
			return
		}

		// Must never panic on arbitrary garbage inputs
		_, _ = adapter.Parse(sessionFile)
		_, _ = adapter.Summarize(sessionFile)
	})
}
