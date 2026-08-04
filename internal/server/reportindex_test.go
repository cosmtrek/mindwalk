package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/judge"
	"github.com/cosmtrek/mindwalk/internal/model"
)

func validReport(eventCount, userTurns int, digest string) *model.Report {
	return &model.Report{
		Version:    1,
		Session:    model.ReportSession{ID: "s", EventCount: eventCount, UserTurns: userTurns},
		Judge:      model.ReportJudge{CLI: "stub", PromptVersion: judge.PromptVersion, InputDigest: digest},
		Dimensions: []model.ReportDimension{{Name: "exploration", Findings: []model.ReportFinding{}}},
	}
}

func TestReportIndexMemoizesAndTracksFileChanges(t *testing.T) {
	cache := judge.Cache{Dir: t.TempDir()}
	if err := cache.Store("key1", validReport(3, 1, "digest-one")); err != nil {
		t.Fatal(err)
	}
	ri := &reportIndex{}
	first := ri.load(cache, "key1")
	if first == nil {
		t.Fatal("stored report not found")
	}
	if second := ri.load(cache, "key1"); second != first {
		t.Fatal("unchanged file was re-read instead of memoized")
	}
	if ri.load(cache, "absent") != nil {
		t.Fatal("absent key returned a report")
	}

	if err := cache.Store("key1", validReport(3, 1, "digest-two-changed")); err != nil {
		t.Fatal(err)
	}
	third := ri.load(cache, "key1")
	if third == nil || third == first || third.Judge.InputDigest != "digest-two-changed" {
		t.Fatalf("changed file not reloaded: %+v", third)
	}
}

func TestReportIndexMarkPresentBeatsListingTTL(t *testing.T) {
	cache := judge.Cache{Dir: t.TempDir()}
	ri := &reportIndex{}
	if ri.load(cache, "k") != nil {
		t.Fatal("empty dir returned a report")
	}
	// The listing is now warm and won't rescan for reportIndexTTL: a report
	// stored by this process must be pushed into the index, not discovered.
	if err := cache.Store("k", validReport(1, 1, "d")); err != nil {
		t.Fatal(err)
	}
	if ri.load(cache, "k") != nil {
		t.Fatal("listing rescanned within TTL; markPresent has nothing to fix")
	}
	ri.markPresent("k")
	if ri.load(cache, "k") == nil {
		t.Fatal("markPresent did not surface the stored report")
	}
}

func TestSessionListReportStatesComeFromIndex(t *testing.T) {
	claudeDir := t.TempDir()
	for _, name := range []string{"scored", "staled", "bare"} {
		writeServerSession(t, filepath.Join(claudeDir, name+".jsonl"),
			`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"`+name+`","cwd":"/tmp","message":{"role":"user","content":"do"}}`,
			`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","sessionId":"`+name+`","cwd":"/tmp","message":{"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":"/tmp/a.go"}}]}}`,
			`{"type":"user","timestamp":"2026-07-09T00:00:02Z","sessionId":"`+name+`","cwd":"/tmp","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"r1","content":"ok","is_error":false}]}}`,
		)
	}
	reportsDir := t.TempDir()

	// Seed reports before the server exists so its first listing sees them.
	seed := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex")})
	seed.reportCache.Dir = reportsDir
	scored, err := seed.findSession("scored")
	if err != nil {
		t.Fatal(err)
	}
	staled, err := seed.findSession("staled")
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.reportCache.Store(scored.Key, validReport(scored.EventCount, scored.UserTurns, "d")); err != nil {
		t.Fatal(err)
	}
	if err := seed.reportCache.Store(staled.Key, validReport(staled.EventCount+1, staled.UserTurns, "d")); err != nil {
		t.Fatal(err)
	}

	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex")})
	s.reportCache.Dir = reportsDir
	resp := httptest.NewRecorder()
	s.handleSessions(resp, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("sessions status = %d", resp.Code)
	}
	var items []sessionListItem
	if err := json.Unmarshal(resp.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, item := range items {
		states[item.ID] = item.ReportState
	}
	if states["scored"] != "done" || states["staled"] != "stale" || states["bare"] != "" {
		t.Fatalf("states = %#v", states)
	}
}
