package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/agents"
	"github.com/cosmtrek/mindwalk/internal/event"
	"github.com/cosmtrek/mindwalk/internal/ingest"
	"github.com/cosmtrek/mindwalk/internal/registry"
)

func TestIngestionStatusEventProjectionAndProvenanceAPIs(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeDir := t.TempDir()
	writeServerSession(t, filepath.Join(claudeDir, "live.jsonl"),
		`{"type":"user","timestamp":"2026-07-13T12:00:00Z","sessionId":"live","cwd":`+quoteJSON(repoRoot)+`,"message":{"role":"user","content":"inspect"}}`,
		`{"type":"assistant","timestamp":"2026-07-13T12:00:01Z","sessionId":"live","cwd":`+quoteJSON(repoRoot)+`,"message":{"role":"assistant","content":[{"type":"tool_use","id":"read","name":"Read","input":{"file_path":`+quoteJSON(filepath.Join(repoRoot, "README.md"))+`}}]}}`,
		`{"type":"user","timestamp":"2026-07-13T12:00:02Z","sessionId":"live","cwd":`+quoteJSON(repoRoot)+`,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"read","content":"synthetic secret=never-persist","is_error":false}]}}`,
	)
	registryPath := filepath.Join(t.TempDir(), "repos.json")
	reg, err := registry.Load(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Add(repoRoot, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	s := New(Config{ClaudeDir: claudeDir, CodexDir: t.TempDir(), RegistryPath: registryPath, DataRoot: t.TempDir(), RegistryOnly: true})
	t.Cleanup(func() { _ = s.ingestion.Close() })
	if err := s.ingestion.PollAll(); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"/api/sources", "/api/ingestion/health", "/api/ingestion/sessions", "/api/quarantine", "/api/sessions/live/projection", "/api/sessions/live/agents"} {
		resp := httptest.NewRecorder()
		s.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, target, nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", target, resp.Code, resp.Body.String())
		}
		if strings.Contains(resp.Body.String(), "never-persist") {
			t.Fatalf("GET %s exposed source content", target)
		}
	}
	agentsResp := httptest.NewRecorder()
	s.Handler().ServeHTTP(agentsResp, httptest.NewRequest(http.MethodGet, "/api/sessions/live/agents", nil))
	var processes []agents.Process
	if err := json.Unmarshal(agentsResp.Body.Bytes(), &processes); err != nil {
		t.Fatal(err)
	}
	if len(processes) != 1 || processes[0].Lifecycle != "UNKNOWN" || processes[0].ControlCapability != "DISPLAY_ONLY" {
		t.Fatalf("agent processes = %#v", processes)
	}

	eventsResp := httptest.NewRecorder()
	s.Handler().ServeHTTP(eventsResp, httptest.NewRequest(http.MethodGet, "/api/sessions/live/events?limit=1", nil))
	if eventsResp.Code != http.StatusOK {
		t.Fatalf("events status = %d: %s", eventsResp.Code, eventsResp.Body.String())
	}
	var page struct {
		Events    []ingest.StreamEvent `json:"events"`
		Latest    int64                `json:"latestSequence"`
		Truncated bool                 `json:"truncated"`
	}
	if err := json.Unmarshal(eventsResp.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Latest < 2 || !page.Truncated || page.Events[0].Sequence != 1 {
		t.Fatalf("event page = %#v", page)
	}

	statuses := s.ingestion.Sessions()
	if len(statuses) != 1 {
		t.Fatalf("ingestion sessions = %#v", statuses)
	}
	all, err := s.ingestion.Events(statuses[0].Key)
	if err != nil || len(all) == 0 {
		t.Fatalf("events = %#v err=%v", all, err)
	}
	provenanceResp := httptest.NewRecorder()
	s.Handler().ServeHTTP(provenanceResp, httptest.NewRequest(http.MethodGet, "/api/events/"+all[0].EventID+"/provenance", nil))
	if provenanceResp.Code != http.StatusOK {
		t.Fatalf("provenance status = %d: %s", provenanceResp.Code, provenanceResp.Body.String())
	}
	var provenance struct {
		EventID    string           `json:"eventId"`
		Provenance event.Provenance `json:"provenance"`
	}
	if err := json.Unmarshal(provenanceResp.Body.Bytes(), &provenance); err != nil {
		t.Fatal(err)
	}
	if provenance.EventID != all[0].EventID || provenance.Provenance.Quality != event.QualityDerived {
		t.Fatalf("provenance = %#v", provenance)
	}
}

func TestIngestionAPIsFailClosedWhenDisabled(t *testing.T) {
	s := New(Config{})
	for _, target := range []string{"/api/sources", "/api/ingestion/health", "/api/ingestion/sessions", "/api/quarantine"} {
		resp := httptest.NewRecorder()
		s.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, target, nil))
		if resp.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s = %d, want 503", target, resp.Code)
		}
	}
}
