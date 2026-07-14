package ingest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/adapter/claudecode"
	"github.com/cosmtrek/mindwalk/internal/adapter/codex"
	"github.com/cosmtrek/mindwalk/internal/registry"
)

func TestManagerIngestsAdaptersResumesAndQuarantinesMetadataOnly(t *testing.T) {
	repoRoot := t.TempDir()
	mustWrite(t, filepath.Join(repoRoot, "README.md"), []byte("# fixture\n"))
	registryPath := filepath.Join(t.TempDir(), "repos.json")
	reg, err := registry.Load(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := reg.Add(repoRoot, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}

	claudeRoot := t.TempDir()
	codexRoot := t.TempDir()
	claudePath := filepath.Join(claudeRoot, "claude-fixture.jsonl")
	codexPath := filepath.Join(codexRoot, "codex-fixture.jsonl")
	appendJSON(t, claudePath,
		map[string]any{"type": "user", "timestamp": "2026-07-13T10:00:00Z", "cwd": repoRoot, "sessionId": "claude-fixture", "message": map[string]any{"role": "user", "content": "inspect"}},
		claudeCall("2026-07-13T10:00:01Z", repoRoot, "claude-fixture", "read-1", "Read", map[string]any{"file_path": filepath.Join(repoRoot, "README.md")}),
		claudeResult("2026-07-13T10:00:02Z", repoRoot, "claude-fixture", "read-1", "synthetic token=secret-fixture-value", false),
	)
	appendJSON(t, codexPath,
		map[string]any{"timestamp": "2026-07-13T11:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "codex-fixture", "session_id": "codex-fixture", "timestamp": "2026-07-13T11:00:00Z", "cwd": repoRoot}},
		codexCall("2026-07-13T11:00:01Z", "call-1", "exec_command", map[string]any{"cmd": "go test ./...", "workdir": repoRoot}),
		codexOutput("2026-07-13T11:00:02Z", "call-1", "PASS synthetic sk-proj-not-a-real-key"),
	)

	dataRoot := t.TempDir()
	cfg := ManagerConfig{
		Sources: []adapter.Source{
			claudecode.Adapter{Dir: claudeRoot},
			codex.Adapter{Dir: codexRoot, IndexPath: filepath.Join(t.TempDir(), "missing-index.jsonl")},
		},
		RegistryPath: registryPath,
		DataRoot:     dataRoot,
	}
	m, err := NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.PollAll(); err != nil {
		t.Fatal(err)
	}

	assertExactSessions(t, m.Sessions(), map[string]string{"claude-fixture": repo.ID, "codex-fixture": repo.ID})
	initial, err := m.Events("")
	if err != nil {
		t.Fatal(err)
	}
	if len(initial) < 5 { // two starts, two tool events, and adapter marks
		t.Fatalf("initial durable events = %d, want at least 5", len(initial))
	}
	ledger, err := os.ReadFile(filepath.Join(dataRoot, "ledger", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-fixture-value", "sk-proj-not-a-real-key"} {
		if bytes.Contains(ledger, []byte(secret)) {
			t.Fatalf("ledger contains raw source secret %q", secret)
		}
	}
	claudeKey := sessionKey(t, m.Sessions(), "claude-fixture")
	projection, err := m.Projection(claudeKey)
	if err != nil {
		t.Fatal(err)
	}
	if projection.RepositoryID != repo.ID || projection.Files["README.md"].Touches != 1 {
		t.Fatalf("projection = %#v", projection)
	}
	replayed, err := m.Projection(claudeKey)
	if err != nil || !reflect.DeepEqual(projection, replayed) {
		t.Fatalf("projection replay = %#v err=%v, want %#v", replayed, err, projection)
	}

	// An incomplete final record is not consumed or accepted.
	incomplete := []byte(`{"type":"user","timestamp":"2026-07-13T10:00:03Z"`)
	appendBytes(t, claudePath, incomplete)
	if err := m.PollAll(); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := m.Events("")
	if len(unchanged) != len(initial) {
		t.Fatalf("incomplete line changed ledger: %d -> %d", len(initial), len(unchanged))
	}
	appendBytes(t, claudePath, []byte(",\"cwd\":"+quote(repoRoot)+",\"sessionId\":\"claude-fixture\",\"message\":{\"role\":\"user\",\"content\":\"continue\"}}\n"))
	if err := m.PollAll(); err != nil {
		t.Fatal(err)
	}
	afterComplete, _ := m.Events("")
	if len(afterComplete) != len(initial)+1 {
		t.Fatalf("completed line durable events = %d, want %d", len(afterComplete), len(initial)+1)
	}

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := m.PollAll(); err != nil {
		t.Fatal(err)
	}
	afterRestart, _ := m.Events("")
	if len(afterRestart) != len(afterComplete) {
		t.Fatalf("restart duplicated events: %d -> %d", len(afterComplete), len(afterRestart))
	}

	// Replacing the file with semantically identical contents is detected but
	// remains exactly-once at the canonical ledger boundary.
	claudeBytes, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(claudePath, claudePath+".rotated"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, claudePath, claudeBytes)
	if err := m.PollAll(); err != nil {
		t.Fatal(err)
	}
	afterReplacement, _ := m.Events("")
	if len(afterReplacement) != len(afterRestart) {
		t.Fatalf("replacement duplicated events: %d -> %d", len(afterRestart), len(afterReplacement))
	}
	replaced := false
	for _, session := range m.Sessions() {
		if session.ID == "claude-fixture" && session.State == StatusReplaced {
			replaced = true
		}
	}
	if !replaced {
		t.Fatalf("replacement status missing from %#v", m.Sessions())
	}

	// Malformed and oversized records are represented only by hashes and sizes.
	malformedSecret := "malformed-secret-fixture"
	appendBytes(t, claudePath, []byte("{\"secret\":\""+malformedSecret+"\"\n"))
	oversizedSecret := "oversized-secret-fixture"
	appendBytes(t, claudePath, []byte(`{"payload":"`+strings.Repeat(oversizedSecret, DefaultMaxLineBytes/len(oversizedSecret)+2)+`"}`+"\n"))
	if err := m.PollAll(); err != nil {
		t.Fatal(err)
	}
	health := m.Health()
	if health.QuarantineCount < 2 {
		t.Fatalf("quarantine count = %d, want at least 2", health.QuarantineCount)
	}
	quarantine, err := os.ReadFile(filepath.Join(dataRoot, "quarantine", "records.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{malformedSecret, oversizedSecret} {
		if bytes.Contains(quarantine, []byte(secret)) {
			t.Fatalf("quarantine contains raw source content %q", secret)
		}
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.Split(bytes.TrimSpace(quarantine), []byte("\n"))[0], &record); err != nil {
		t.Fatal(err)
	}
	if _, exists := record["raw"]; exists {
		t.Fatal("quarantine record exposes raw content")
	}
	if err := os.Remove(claudePath); err != nil {
		t.Fatal(err)
	}
	if err := m.PollAll(); err != nil {
		t.Fatal(err)
	}
	if state := sessionState(t, m.Sessions(), "claude-fixture"); state != StatusMissing {
		t.Fatalf("deleted source state = %s, want %s", state, StatusMissing)
	}

	items, latest, truncated, err := m.EventsAfter(0, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !truncated || items[0].Sequence != 1 || items[1].Sequence != 2 || latest != m.Health().DurableSequence {
		t.Fatalf("bounded replay = items %#v latest %d truncated %t", items, latest, truncated)
	}
}

func TestManagerConfinesSourcesAndLeavesUnprovenAssociationUnknown(t *testing.T) {
	repoRoot := t.TempDir()
	registryPath := filepath.Join(t.TempDir(), "repos.json")
	reg, err := registry.Load(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Add(repoRoot, "registered"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}

	sourceRoot := t.TempDir()
	unknownRoot := t.TempDir()
	unknownPath := filepath.Join(sourceRoot, "unknown.jsonl")
	appendJSON(t, unknownPath, map[string]any{
		"type": "user", "timestamp": "2026-07-13T12:00:00Z", "cwd": unknownRoot,
		"sessionId": "unknown", "message": map[string]any{"role": "user", "content": "synthetic"},
	})
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	appendJSON(t, outside, map[string]any{
		"type": "user", "timestamp": "2026-07-13T12:00:00Z", "cwd": repoRoot,
		"sessionId": "escaped", "message": map[string]any{"role": "user", "content": "synthetic"},
	})
	if err := os.Symlink(outside, filepath.Join(sourceRoot, "escaped.jsonl")); err != nil {
		t.Fatal(err)
	}

	m, err := NewManager(ManagerConfig{
		Sources:      []adapter.Source{claudecode.Adapter{Dir: sourceRoot}},
		RegistryPath: registryPath,
		DataRoot:     t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := m.PollAll(); err != nil {
		t.Fatal(err)
	}
	sessions := m.Sessions()
	if len(sessions) != 1 || sessions[0].ID != "unknown" || sessions[0].Association != "UNKNOWN" || sessions[0].RepositoryID != "" {
		t.Fatalf("sessions = %#v", sessions)
	}
	events, err := m.Events("")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("unassociated or escaped events persisted: %#v", events)
	}
	if m.Health().QuarantineCount != 1 {
		t.Fatalf("quarantine count = %d, want 1", m.Health().QuarantineCount)
	}
}

func sessionKey(t *testing.T, sessions []SessionStatus, id string) string {
	t.Helper()
	for _, session := range sessions {
		if session.ID == id {
			return session.Key
		}
	}
	t.Fatalf("session %s not found", id)
	return ""
}

func sessionState(t *testing.T, sessions []SessionStatus, id string) Status {
	t.Helper()
	for _, session := range sessions {
		if session.ID == id {
			return session.State
		}
	}
	t.Fatalf("session %s not found", id)
	return ""
}

func assertExactSessions(t *testing.T, sessions []SessionStatus, want map[string]string) {
	t.Helper()
	for id, repoID := range want {
		found := false
		for _, session := range sessions {
			if session.ID == id {
				found = true
				if session.Association != "EXACT" || session.RepositoryID != repoID {
					t.Fatalf("session %s = %#v", id, session)
				}
			}
		}
		if !found {
			t.Fatalf("session %s not found in %#v", id, sessions)
		}
	}
}

func claudeCall(timestamp, cwd, sessionID, id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "assistant", "timestamp": timestamp, "cwd": cwd, "sessionId": sessionID, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}}
}

func claudeResult(timestamp, cwd, sessionID, id, content string, failed bool) map[string]any {
	return map[string]any{"type": "user", "timestamp": timestamp, "cwd": cwd, "sessionId": sessionID, "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": content, "is_error": failed}}}}
}

func codexCall(timestamp, callID, name string, args map[string]any) map[string]any {
	b, _ := json.Marshal(args)
	return map[string]any{"timestamp": timestamp, "type": "response_item", "payload": map[string]any{"type": "function_call", "id": "fc-" + callID, "call_id": callID, "name": name, "arguments": string(b)}}
}

func codexOutput(timestamp, callID, output string) map[string]any {
	return map[string]any{"timestamp": timestamp, "type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": callID, "output": output}}
}

func appendJSON(t *testing.T, path string, values ...map[string]any) {
	t.Helper()
	var b bytes.Buffer
	for _, value := range values {
		line, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	appendBytes(t, path, b.Bytes())
}

func appendBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func quote(value string) string {
	b, _ := json.Marshal(value)
	return string(b)
}
