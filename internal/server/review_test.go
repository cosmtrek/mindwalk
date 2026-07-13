package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/review"
)

func TestReviewAndComparisonAPIsUseRedactedNormalizedSessions(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()
	left := filepath.Join(dir, "left.jsonl")
	right := filepath.Join(dir, "right.jsonl")
	writeServerSession(t, left,
		`{"type":"user","timestamp":"2026-07-13T00:00:00Z","sessionId":"left","cwd":`+quoteJSON(repo)+`,"message":{"role":"user","content":"inspect"}}`,
		`{"type":"assistant","timestamp":"2026-07-13T00:00:01Z","sessionId":"left","cwd":`+quoteJSON(repo)+`,"message":{"role":"assistant","content":[{"type":"tool_use","id":"edit","name":"Edit","input":{"file_path":`+quoteJSON(filepath.Join(repo, "a.go"))+`,"old_string":"token=synthetic-review-secret","new_string":"safe"}}]}}`,
		`{"type":"user","timestamp":"2026-07-13T00:00:02Z","sessionId":"left","cwd":`+quoteJSON(repo)+`,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"edit","content":"failed","is_error":true}]}}`,
	)
	writeServerSession(t, right,
		`{"type":"user","timestamp":"2026-07-13T00:01:00Z","sessionId":"right","cwd":`+quoteJSON(repo)+`,"message":{"role":"user","content":"inspect"}}`,
		`{"type":"assistant","timestamp":"2026-07-13T00:01:01Z","sessionId":"right","cwd":`+quoteJSON(repo)+`,"message":{"role":"assistant","content":[{"type":"tool_use","id":"read","name":"Read","input":{"file_path":`+quoteJSON(filepath.Join(repo, "b.go"))+`}}]}}`,
		`{"type":"user","timestamp":"2026-07-13T00:01:02Z","sessionId":"right","cwd":`+quoteJSON(repo)+`,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"read","content":"ok","is_error":false}]}}`,
	)
	s := New(Config{ClaudeDir: dir, CodexDir: t.TempDir()})

	reviewResp := httptest.NewRecorder()
	s.Handler().ServeHTTP(reviewResp, httptest.NewRequest(http.MethodGet, "/api/sessions/left/review", nil))
	if reviewResp.Code != http.StatusOK {
		t.Fatalf("review = %d %s", reviewResp.Code, reviewResp.Body.String())
	}
	var packet review.SessionReview
	if err := json.Unmarshal(reviewResp.Body.Bytes(), &packet); err != nil {
		t.Fatal(err)
	}
	if packet.SessionID != "left" || packet.Errors != 1 || len(packet.EditedFiles) != 1 {
		t.Fatalf("packet = %#v", packet)
	}

	markdownResp := httptest.NewRecorder()
	s.Handler().ServeHTTP(markdownResp, httptest.NewRequest(http.MethodGet, "/api/sessions/left/review?format=markdown", nil))
	if markdownResp.Code != http.StatusOK || bytes.Contains(markdownResp.Body.Bytes(), []byte("synthetic-review-secret")) || !bytes.Contains(markdownResp.Body.Bytes(), []byte("Provenance: derived")) {
		t.Fatalf("markdown = %d %s", markdownResp.Code, markdownResp.Body.String())
	}

	compareResp := httptest.NewRecorder()
	target := "/api/compare?left=" + url.QueryEscape("left") + "&right=" + url.QueryEscape("right")
	s.Handler().ServeHTTP(compareResp, httptest.NewRequest(http.MethodGet, target, nil))
	if compareResp.Code != http.StatusOK {
		t.Fatalf("compare = %d %s", compareResp.Code, compareResp.Body.String())
	}
	if bytes.Contains(compareResp.Body.Bytes(), []byte(`"sharedFiles":null`)) || bytes.Contains(compareResp.Body.Bytes(), []byte(`"onlyLeft":null`)) || bytes.Contains(compareResp.Body.Bytes(), []byte(`"onlyRight":null`)) {
		t.Fatalf("comparison arrays must not be null: %s", compareResp.Body.String())
	}
	var comparison review.Comparison
	if err := json.Unmarshal(compareResp.Body.Bytes(), &comparison); err != nil {
		t.Fatal(err)
	}
	if comparison.MemoryStatus != "UNAVAILABLE" || len(comparison.OnlyLeft) != 1 || len(comparison.OnlyRight) != 1 {
		t.Fatalf("comparison = %#v", comparison)
	}
}
