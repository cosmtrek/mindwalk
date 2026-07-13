package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cosmtrek/mindwalk/internal/registry"
)

func TestSessionStreamSequenceResumeAndHeartbeat(t *testing.T) {
	claudeDir := t.TempDir()
	repoRoot := t.TempDir()
	session := filepath.Join(claudeDir, "stream.jsonl")
	writeServerSession(t, session,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"stream","cwd":`+quoteJSON(repoRoot)+`,"message":{"role":"user","content":"hello"}}`,
	)
	registryPath := filepath.Join(t.TempDir(), "repos.json")
	reg, err := registry.Load(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Add(repoRoot, "stream"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	s := New(Config{ClaudeDir: claudeDir, CodexDir: t.TempDir(), RegistryPath: registryPath, DataRoot: t.TempDir()})
	t.Cleanup(func() { _ = s.ingestion.Close() })
	if err := s.ingestion.PollAll(); err != nil {
		t.Fatal(err)
	}
	s.streamPoll = 5 * time.Millisecond
	s.streamHeartbeat = 7 * time.Millisecond
	s.streamBatch = 1
	bounded := captureStream(t, s, "", 5*time.Millisecond)
	if !strings.Contains(bounded, `"status":"replay-bounded"`) {
		t.Fatalf("bounded replay stream = %q", bounded)
	}
	s.streamBatch = 256

	first := captureStream(t, s, "", 18*time.Millisecond)
	if !strings.Contains(first, "event: observable") || !strings.Contains(first, `"eventType":"session.started"`) || !strings.Contains(first, ": heartbeat ") {
		t.Fatalf("initial stream = %q", first)
	}
	cursor := s.ingestion.Health().DurableSequence

	appendServerSession(t, session,
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","sessionId":"stream","cwd":`+quoteJSON(repoRoot)+`,"message":{"role":"assistant","content":[{"type":"tool_use","id":"read-1","name":"Read","input":{"file_path":`+quoteJSON(filepath.Join(repoRoot, "README.md"))+`}}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:02Z","sessionId":"stream","cwd":`+quoteJSON(repoRoot)+`,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"read-1","content":"ok","is_error":false}]}}`,
	)
	if err := s.ingestion.PollAll(); err != nil {
		t.Fatal(err)
	}
	resumed := captureStream(t, s, strconv.FormatInt(cursor, 10), 14*time.Millisecond)
	if !strings.Contains(resumed, "event: observable") || !strings.Contains(resumed, `"eventType":"file.read"`) {
		t.Fatalf("resumed stream = %q", resumed)
	}

	currentCursor := s.ingestion.Health().DurableSequence
	current := captureStream(t, s, strconv.FormatInt(currentCursor, 10), 10*time.Millisecond)
	if strings.Contains(current, "event: observable") || !strings.Contains(current, ": heartbeat ") {
		t.Fatalf("current cursor stream = %q", current)
	}
}

func captureStream(t *testing.T, s *Server, cursor string, duration time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/stream/stream", nil).WithContext(ctx)
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	resp := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.handleSessionResource(resp, req)
		close(done)
	}()
	timer := time.NewTimer(duration)
	<-timer.C
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
	return resp.Body.String()
}
