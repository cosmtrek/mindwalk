package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmtrek/mindwalk/internal/judge"
	"github.com/cosmtrek/mindwalk/internal/model"
)

type stubJudge struct {
	output string
	err    error
}

func (s stubJudge) Run(ctx context.Context, prompt, input string) (judge.RunResult, error) {
	return judge.RunResult{Text: s.output, Model: "stub-model"}, s.err
}

func (s stubJudge) Name() string { return "stub" }

const stubJudgeOutput = `{"task_summary":"demo","dimensions":[
{"name":"exploration","findings":[{"claim":"读了再改","severity":"info","evidence_seqs":[0]}]},
{"name":"scope","findings":[]},
{"name":"wandering","findings":[]},
{"name":"verification","findings":[{"claim":"改后未验证","severity":"warning","evidence_seqs":[0]}]}],
"notable_moments":[],"narrative":"ok"}`

func TestAnalyzeEndpointRunsJudgeAndServesReport(t *testing.T) {
	claudeDir := t.TempDir()
	writeServerSession(t, filepath.Join(claudeDir, "eval.jsonl"),
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"eval","cwd":"/tmp","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","sessionId":"eval","cwd":"/tmp","message":{"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":"/tmp/a.go"}}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:02Z","sessionId":"eval","cwd":"/tmp","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"r1","content":"ok","is_error":false}]}}`,
	)
	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex"), OmpDir: filepath.Join(t.TempDir(), "omp")})
	s.analyze.runner = stubJudge{output: stubJudgeOutput}
	s.reportCache.Dir = t.TempDir()

	// before any run: state none, judge reported available via the stub
	getResp := httptest.NewRecorder()
	s.handleSessionResource(getResp, httptest.NewRequest(http.MethodGet, "/api/sessions/eval/report", nil))
	if getResp.Code != http.StatusOK {
		t.Fatalf("report status = %d body=%s", getResp.Code, getResp.Body.String())
	}
	var status reportStatus
	if err := json.Unmarshal(getResp.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "none" || !status.JudgeAvailable || status.JudgeCLI != "stub" {
		t.Fatalf("status = %+v", status)
	}

	postResp := httptest.NewRecorder()
	s.handleSessionResource(postResp, httptest.NewRequest(http.MethodPost, "/api/sessions/eval/analyze", nil))
	if postResp.Code != http.StatusAccepted {
		t.Fatalf("analyze status = %d body=%s", postResp.Code, postResp.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := httptest.NewRecorder()
		s.handleSessionResource(resp, httptest.NewRequest(http.MethodGet, "/api/sessions/eval/report", nil))
		if err := json.Unmarshal(resp.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if status.State == "done" || status.State == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never finished: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.State != "done" || status.Stale || status.Report == nil {
		t.Fatalf("status = %+v", status)
	}
	verdicts := map[string]string{}
	for _, dim := range status.Report.Dimensions {
		verdicts[dim.Name] = dim.Verdict
	}
	if verdicts["verification"] != model.VerdictWarning || verdicts["exploration"] != model.VerdictGood {
		t.Fatalf("verdicts = %#v", verdicts)
	}

	// the report must survive a server restart via the disk cache
	restarted := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex"), OmpDir: filepath.Join(t.TempDir(), "omp")})
	restarted.analyze.runner = stubJudge{output: stubJudgeOutput}
	restarted.reportCache.Dir = s.reportCache.Dir
	resp := httptest.NewRecorder()
	restarted.handleSessionResource(resp, httptest.NewRequest(http.MethodGet, "/api/sessions/eval/report", nil))
	if err := json.Unmarshal(resp.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "done" || status.Report == nil {
		t.Fatalf("restarted status = %+v", status)
	}
}

func TestAnalyzeEndpointReportsJudgeFailure(t *testing.T) {
	claudeDir := t.TempDir()
	writeServerSession(t, filepath.Join(claudeDir, "fail.jsonl"),
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"fail","cwd":"/tmp","message":{"role":"user","content":"x"}}`,
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","sessionId":"fail","cwd":"/tmp","message":{"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":"/tmp/a.go"}}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:02Z","sessionId":"fail","cwd":"/tmp","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"r1","content":"ok","is_error":false}]}}`,
	)
	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex"), OmpDir: filepath.Join(t.TempDir(), "omp")})
	s.analyze.runner = stubJudge{output: "garbage, not json"}
	s.reportCache.Dir = t.TempDir()

	postResp := httptest.NewRecorder()
	s.handleSessionResource(postResp, httptest.NewRequest(http.MethodPost, "/api/sessions/fail/analyze", nil))
	if postResp.Code != http.StatusAccepted {
		t.Fatalf("analyze status = %d", postResp.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	var status reportStatus
	for {
		resp := httptest.NewRecorder()
		s.handleSessionResource(resp, httptest.NewRequest(http.MethodGet, "/api/sessions/fail/report", nil))
		if err := json.Unmarshal(resp.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if status.State == "failed" || status.State == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never finished: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.State != "failed" || status.Error == "" {
		t.Fatalf("status = %+v", status)
	}
}

// gateJudge blocks every call until released, so tests can observe the
// running state deterministically.
type gateJudge struct {
	release chan struct{}
	output  string
}

func (g gateJudge) Run(ctx context.Context, prompt, input string) (judge.RunResult, error) {
	select {
	case <-g.release:
	case <-ctx.Done():
		return judge.RunResult{}, ctx.Err()
	}
	return judge.RunResult{Text: g.output, Model: "stub-model"}, nil
}

func (g gateJudge) Name() string { return "stub" }

func TestAnalyzeRubricFalseBypassesReportCache(t *testing.T) {
	claudeDir := t.TempDir()
	writeServerSession(t, filepath.Join(claudeDir, "eval.jsonl"),
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"eval","cwd":"/tmp","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","sessionId":"eval","cwd":"/tmp","message":{"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":"/tmp/a.go"}}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:02Z","sessionId":"eval","cwd":"/tmp","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"r1","content":"ok","is_error":false}]}}`,
	)
	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex"), OmpDir: filepath.Join(t.TempDir(), "omp")})
	s.analyze.runner = stubJudge{output: stubJudgeOutput}
	s.reportCache.Dir = t.TempDir()

	meta, err := s.findSession("eval")
	if err != nil {
		t.Fatal(err)
	}
	// A richer, rubric-ful report already sits in the cache; a rubric:false
	// run must not downgrade it.
	rich := &model.Report{
		Version:    1,
		Session:    model.ReportSession{ID: "eval", EventCount: 1},
		Judge:      model.ReportJudge{CLI: "stub", PromptVersion: judge.PromptVersion, RubricPromptVersion: judge.RubricPromptVersion},
		Dimensions: []model.ReportDimension{{Name: "exploration", Verdict: model.VerdictGood, Findings: []model.ReportFinding{}}},
		Rubric:     &model.Rubric{Status: model.RubricStatusScored},
	}
	if err := s.reportCache.Store(meta.Key, rich); err != nil {
		t.Fatal(err)
	}

	postResp := httptest.NewRecorder()
	s.handleSessionResource(postResp, httptest.NewRequest(http.MethodPost, "/api/sessions/eval/analyze", strings.NewReader(`{"rubric":false}`)))
	if postResp.Code != http.StatusAccepted {
		t.Fatalf("analyze status = %d body=%s", postResp.Code, postResp.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	var status reportStatus
	for {
		resp := httptest.NewRecorder()
		s.handleSessionResource(resp, httptest.NewRequest(http.MethodGet, "/api/sessions/eval/report", nil))
		if err := json.Unmarshal(resp.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if status.State == "done" || status.State == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never finished: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The requester gets their dimensions-only result…
	if status.State != "done" || status.Report == nil || status.Report.Rubric != nil {
		t.Fatalf("status = %+v", status)
	}
	// …and the richer cached report survives untouched on disk.
	if cached := s.reportCache.Load(meta.Key); cached == nil || cached.Rubric == nil {
		t.Fatalf("rubric:false overwrote the richer cache: %#v", cached)
	}
}

func TestAnalyzeConflictingConfigRejected(t *testing.T) {
	claudeDir := t.TempDir()
	writeServerSession(t, filepath.Join(claudeDir, "eval.jsonl"),
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"eval","cwd":"/tmp","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","sessionId":"eval","cwd":"/tmp","message":{"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":"/tmp/a.go"}}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:02Z","sessionId":"eval","cwd":"/tmp","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"r1","content":"ok","is_error":false}]}}`,
	)
	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex"), OmpDir: filepath.Join(t.TempDir(), "omp")})
	gate := gateJudge{release: make(chan struct{}), output: stubJudgeOutput}
	s.analyze.runner = gate
	s.reportCache.Dir = t.TempDir()

	first := httptest.NewRecorder()
	s.handleSessionResource(first, httptest.NewRequest(http.MethodPost, "/api/sessions/eval/analyze", nil))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first analyze = %d", first.Code)
	}
	// Same session, different configuration: the API must refuse rather than
	// pretend the in-flight run covers it.
	conflict := httptest.NewRecorder()
	s.handleSessionResource(conflict, httptest.NewRequest(http.MethodPost, "/api/sessions/eval/analyze", strings.NewReader(`{"rubric":false}`)))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting config = %d, want 409", conflict.Code)
	}
	// Same configuration deduplicates as before.
	same := httptest.NewRecorder()
	s.handleSessionResource(same, httptest.NewRequest(http.MethodPost, "/api/sessions/eval/analyze", nil))
	if same.Code != http.StatusAccepted {
		t.Fatalf("same config = %d, want 202", same.Code)
	}
	close(gate.release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := httptest.NewRecorder()
		var status reportStatus
		s.handleSessionResource(resp, httptest.NewRequest(http.MethodGet, "/api/sessions/eval/report", nil))
		if err := json.Unmarshal(resp.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if status.State == "done" || status.State == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job never finished after release")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAnalyzeEndpointRejectsBadBodies(t *testing.T) {
	claudeDir := t.TempDir()
	writeServerSession(t, filepath.Join(claudeDir, "eval.jsonl"),
		`{"type":"user","timestamp":"2026-07-14T00:00:00Z","sessionId":"eval","cwd":"/tmp","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"assistant","timestamp":"2026-07-14T00:00:01Z","sessionId":"eval","cwd":"/tmp","message":{"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":"/tmp/a.go"}}]}}`,
		`{"type":"user","timestamp":"2026-07-14T00:00:02Z","sessionId":"eval","cwd":"/tmp","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"r1","content":"ok","is_error":false}]}}`,
	)
	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex"), OmpDir: filepath.Join(t.TempDir(), "omp")})
	s.analyze.runner = stubJudge{output: stubJudgeOutput}
	s.reportCache.Dir = t.TempDir()

	// This POST starts an expensive run: garbled JSON and trailing garbage
	// must both be rejected, not silently run with defaults.
	for _, body := range []string{`{bad`, `{"cli":"stub"} trailing`} {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/eval/analyze", strings.NewReader(body))
		s.handleSessionResource(resp, req)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("body %q -> %d, want 400", body, resp.Code)
		}
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/eval/analyze", strings.NewReader(`{"cli":"gemini"}`))
	s.handleSessionResource(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("unknown cli -> %d, want 400", resp.Code)
	}
}
