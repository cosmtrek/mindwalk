package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestTraceStillLoadsWhenSessionCwdIsMissing(t *testing.T) {
	claudeDir := t.TempDir()
	missingRoot := filepath.Join(t.TempDir(), "deleted-repo")
	session := filepath.Join(claudeDir, "missingcwd.jsonl")
	writeServerSession(t, session,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"missingcwd","cwd":`+quoteJSON(missingRoot)+`,"message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","sessionId":"missingcwd","cwd":`+quoteJSON(missingRoot)+`,"message":{"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":`+quoteJSON(filepath.Join(missingRoot, "a.go"))+`}}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:02Z","sessionId":"missingcwd","cwd":`+quoteJSON(missingRoot)+`,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"r1","content":"ok","is_error":false}]}}`,
	)

	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex")})
	traceResp := httptest.NewRecorder()
	s.handleSessionResource(traceResp, httptest.NewRequest(http.MethodGet, "/api/sessions/missingcwd/trace", nil))
	if traceResp.Code != http.StatusOK {
		t.Fatalf("trace status = %d body=%s", traceResp.Code, traceResp.Body.String())
	}
	var trace model.Trace
	if err := json.Unmarshal(traceResp.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if len(trace.Events) != 1 || trace.Stats.FilesInRepo != 0 {
		t.Fatalf("trace = %#v", trace)
	}

	cityResp := httptest.NewRecorder()
	s.handleSessionResource(cityResp, httptest.NewRequest(http.MethodGet, "/api/sessions/missingcwd/citymap", nil))
	if cityResp.Code != http.StatusOK {
		t.Fatalf("citymap status = %d body=%s", cityResp.Code, cityResp.Body.String())
	}
	var city model.CityMap
	if err := json.Unmarshal(cityResp.Body.Bytes(), &city); err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 0 || city.Repo.Root == "" || city.Layout.Algorithm != "unavailable" {
		t.Fatalf("city = %#v", city)
	}

	snapshotResp := httptest.NewRecorder()
	s.handleSessionResource(snapshotResp, httptest.NewRequest(http.MethodGet, "/api/sessions/missingcwd/snapshot", nil))
	if snapshotResp.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d body=%s", snapshotResp.Code, snapshotResp.Body.String())
	}
	var snapshot struct {
		Trace model.Trace   `json:"trace"`
		City  model.CityMap `json:"city"`
	}
	if err := json.Unmarshal(snapshotResp.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Trace.Events) != 1 || snapshot.City.Repo.Root != city.Repo.Root {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestOpenSessionUsesUniqueKeyAndFindSessionAcceptsBasename(t *testing.T) {
	claudeDir := t.TempDir()
	session := filepath.Join(claudeDir, "renamed.jsonl")
	writeServerSession(t, session,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"internal-id","cwd":"/tmp","message":{"role":"user","content":"hello"}}`,
	)

	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex"), OpenSession: session})
	wantKey := adapter.SessionKey("claude-code", session)
	if got := s.openSessionKey(); got != wantKey {
		t.Fatalf("openSessionKey = %q, want %q", got, wantKey)
	}
	meta, err := s.findSession("renamed")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != "internal-id" {
		t.Fatalf("meta = %#v", meta)
	}
}

func TestDuplicateSessionIDsUseDistinctKeysAndCaches(t *testing.T) {
	claudeDir := t.TempDir()
	codexDir := t.TempDir()
	repoRoot := t.TempDir()
	first := filepath.Join(codexDir, "first.jsonl")
	second := filepath.Join(codexDir, "second.jsonl")
	for i, path := range []string{first, second} {
		writeServerJSONL(t, path, map[string]any{
			"timestamp": "2026-07-09T00:00:0" + strconv.Itoa(i) + "Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":  "shared-id",
				"cwd": filepath.ToSlash(repoRoot),
			},
		})
	}

	s := New(Config{ClaudeDir: claudeDir, CodexDir: codexDir})
	sessions, err := s.listSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %#v", sessions)
	}
	if sessions[0].Key == "" || sessions[1].Key == "" || sessions[0].Key == sessions[1].Key {
		t.Fatalf("session keys are not unique: %#v", sessions)
	}

	for _, session := range sessions {
		trace, _, err := s.traceAndMap(session.Key)
		if err != nil {
			t.Fatal(err)
		}
		if trace.Session.Path != session.Path {
			t.Fatalf("key %q loaded %q, want %q", session.Key, trace.Session.Path, session.Path)
		}
	}
	if len(s.traces) != 2 {
		t.Fatalf("trace cache entries = %d, want 2", len(s.traces))
	}
	if _, err := s.findSession("shared-id"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate legacy ID error = %v", err)
	}
}

func TestTraceCacheReloadsWhenActiveSessionGrows(t *testing.T) {
	claudeDir := t.TempDir()
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "a.go"), []byte("package demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "b.go"), []byte("package demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(claudeDir, "growing.jsonl")
	writeServerSession(t, session,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"growing","cwd":`+quoteJSON(repoRoot)+`,"message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","timestamp":"2026-07-09T00:00:01Z","sessionId":"growing","cwd":`+quoteJSON(repoRoot)+`,"message":{"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":`+quoteJSON(filepath.Join(repoRoot, "a.go"))+`}}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:02Z","sessionId":"growing","cwd":`+quoteJSON(repoRoot)+`,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"r1","content":"ok","is_error":false}]}}`,
	)

	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex")})
	firstTrace, firstCity, err := s.traceAndMap("growing")
	if err != nil {
		t.Fatal(err)
	}
	if len(firstTrace.Events) != 1 {
		t.Fatalf("initial events = %d, want 1", len(firstTrace.Events))
	}

	appendServerSession(t, session,
		`{"type":"assistant","timestamp":"2026-07-09T00:00:03Z","sessionId":"growing","cwd":`+quoteJSON(repoRoot)+`,"message":{"role":"assistant","content":[{"type":"tool_use","id":"r2","name":"Read","input":{"file_path":`+quoteJSON(filepath.Join(repoRoot, "b.go"))+`}}]}}`,
		`{"type":"user","timestamp":"2026-07-09T00:00:04Z","sessionId":"growing","cwd":`+quoteJSON(repoRoot)+`,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"r2","content":"ok","is_error":false}]}}`,
	)

	secondTrace, secondCity, err := s.traceAndMap("growing")
	if err != nil {
		t.Fatal(err)
	}
	if len(secondTrace.Events) != 2 {
		t.Fatalf("events after append = %d, want 2", len(secondTrace.Events))
	}
	if secondTrace == firstTrace {
		t.Fatal("trace cache was reused after the session file changed")
	}
	if secondCity == firstCity {
		t.Fatal("city cache was reused after the session file changed")
	}
}

func TestSessionsFreshBypassesListTTL(t *testing.T) {
	claudeDir := t.TempDir()
	writeServerSession(t, filepath.Join(claudeDir, "first.jsonl"),
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"first","cwd":"/tmp","message":{"role":"user","content":"hello"}}`,
	)
	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex")})

	initial := requestSessions(t, s, "/api/sessions")
	if len(initial) != 1 {
		t.Fatalf("initial sessions = %d, want 1", len(initial))
	}
	writeServerSession(t, filepath.Join(claudeDir, "second.jsonl"),
		`{"type":"user","timestamp":"2026-07-09T00:00:01Z","sessionId":"second","cwd":"/tmp","message":{"role":"user","content":"hello"}}`,
	)

	cached := requestSessions(t, s, "/api/sessions")
	if len(cached) != 1 {
		t.Fatalf("cached sessions = %d, want 1", len(cached))
	}
	fresh := requestSessions(t, s, "/api/sessions?fresh=1")
	if len(fresh) != 2 {
		t.Fatalf("fresh sessions = %d, want 2", len(fresh))
	}
}

func TestConcurrentFreshGenerationReusesCompletedScan(t *testing.T) {
	claudeDir := t.TempDir()
	writeServerSession(t, filepath.Join(claudeDir, "session.jsonl"),
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"session","cwd":"/tmp","message":{"role":"user","content":"hello"}}`,
	)
	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex")})

	// Two fresh callers that enter before either scan completes observe the
	// same generation. The second must reuse the first completed scan.
	observed := s.freshGen
	if _, err := s.listSessionsObserved(true, observed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.listSessionsObserved(true, observed); err != nil {
		t.Fatal(err)
	}
	if s.freshGen != observed+1 {
		t.Fatalf("fresh generation = %d, want %d", s.freshGen, observed+1)
	}
}

func TestInflightLoadsShareOrAdvanceFileSnapshots(t *testing.T) {
	t.Run("same fingerprint shares one snapshot", func(t *testing.T) {
		s, source, session := newBlockingServer(t)
		owner := make(chan traceMapResult, 1)
		waiter := make(chan traceMapResult, 1)
		go func() {
			trace, city, err := s.traceAndMap("blocking")
			owner <- traceMapResult{trace: trace, city: city, err: err}
		}()
		<-source.started
		go func() {
			trace, city, err := s.traceAndMap("blocking")
			waiter <- traceMapResult{trace: trace, city: city, err: err}
		}()
		close(source.release)

		first, second := <-owner, <-waiter
		if first.err != nil || second.err != nil {
			t.Fatalf("owner error=%v waiter error=%v", first.err, second.err)
		}
		if first.trace != second.trace || first.city != second.city {
			t.Fatal("same file version did not share one trace/city snapshot")
		}
		if got := source.parses.Load(); got != 1 {
			t.Fatalf("parse count = %d, want 1", got)
		}
		_ = session
	})

	t.Run("new fingerprint reloads after older inflight", func(t *testing.T) {
		s, source, session := newBlockingServer(t)
		owner := make(chan traceMapResult, 1)
		newer := make(chan traceMapResult, 1)
		go func() {
			trace, city, err := s.traceAndMap("blocking")
			owner <- traceMapResult{trace: trace, city: city, err: err}
		}()
		<-source.started
		appendServerSession(t, session, "v2")
		go func() {
			trace, city, err := s.traceAndMap("blocking")
			newer <- traceMapResult{trace: trace, city: city, err: err}
		}()
		close(source.release)

		first, second := <-owner, <-newer
		if first.err != nil || second.err != nil {
			t.Fatalf("owner error=%v newer error=%v", first.err, second.err)
		}
		if first.trace.Session.Title != "v1" || second.trace.Session.Title != "v1\nv2" {
			t.Fatalf("titles = %q then %q", first.trace.Session.Title, second.trace.Session.Title)
		}
		if got := source.parses.Load(); got != 2 {
			t.Fatalf("parse count = %d, want 2", got)
		}
	})
}

func TestServerLoadsCodexSessions(t *testing.T) {
	claudeDir := t.TempDir()
	codexDir := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(codexDir, "codex.jsonl")
	writeServerJSONL(t, session,
		map[string]any{
			"timestamp": "2026-07-09T00:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":  "codex-server",
				"cwd": filepath.ToSlash(root),
			},
		},
		map[string]any{
			"timestamp": "2026-07-09T00:00:01Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":      "function_call",
				"id":        "fc",
				"name":      "exec_command",
				"arguments": `{"cmd":"sed -n '1,20p' README.md","workdir":` + quoteJSON(root) + `}`,
				"call_id":   "call",
			},
		},
		map[string]any{
			"timestamp": "2026-07-09T00:00:02Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "function_call_output",
				"call_id": "call",
				"output":  "Chunk ID: x\nProcess exited with code 0\nOutput:\n# Demo\n",
			},
		},
	)

	s := New(Config{ClaudeDir: claudeDir, CodexDir: codexDir})
	sessionsResp := httptest.NewRecorder()
	s.handleSessions(sessionsResp, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if sessionsResp.Code != http.StatusOK {
		t.Fatalf("sessions status = %d body=%s", sessionsResp.Code, sessionsResp.Body.String())
	}
	var sessions []model.SessionMeta
	if err := json.Unmarshal(sessionsResp.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "codex-server" || sessions[0].Harness != "codex" {
		t.Fatalf("sessions = %#v", sessions)
	}

	traceResp := httptest.NewRecorder()
	s.handleSessionResource(traceResp, httptest.NewRequest(http.MethodGet, "/api/sessions/codex-server/trace", nil))
	if traceResp.Code != http.StatusOK {
		t.Fatalf("trace status = %d body=%s", traceResp.Code, traceResp.Body.String())
	}
	var trace model.Trace
	if err := json.Unmarshal(traceResp.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.Session.Harness != "codex" || len(trace.Events) != 1 || trace.Events[0].Targets[0].Path != "README.md" {
		t.Fatalf("trace = %#v", trace)
	}
}

func TestServerSkipsClaudeSubagentSessions(t *testing.T) {
	claudeDir := t.TempDir()
	session := filepath.Join(claudeDir, "main.jsonl")
	subagent := filepath.Join(claudeDir, "subagents", "agent-child.jsonl")
	if err := os.MkdirAll(filepath.Dir(subagent), 0o755); err != nil {
		t.Fatal(err)
	}
	writeServerSession(t, session,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"main","cwd":"/tmp","message":{"role":"user","content":"hello"}}`,
	)
	writeServerSession(t, subagent,
		`{"type":"user","timestamp":"2026-07-09T00:00:01Z","sessionId":"subagent","cwd":"/tmp","message":{"role":"user","content":"internal"}}`,
	)

	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex")})
	sessions, err := s.scanSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "main" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestServerSkipsCodexSubagentSessions(t *testing.T) {
	claudeDir := t.TempDir()
	codexDir := t.TempDir()
	mainSession := filepath.Join(codexDir, "main.jsonl")
	subagentSession := filepath.Join(codexDir, "subagent.jsonl")
	writeServerJSONL(t, mainSession, map[string]any{
		"timestamp": "2026-07-10T00:00:00Z",
		"type":      "session_meta",
		"payload": map[string]any{
			"id":     "main-thread",
			"cwd":    "/tmp",
			"source": "vscode",
		},
	})
	writeServerJSONL(t, subagentSession,
		map[string]any{
			"timestamp": "2026-07-10T00:00:01Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":  "child-thread",
				"cwd": "/tmp",
				"source": map[string]any{
					"subagent": map[string]any{
						"thread_spawn": map[string]any{"parent_thread_id": "main-thread"},
					},
				},
			},
		},
		map[string]any{
			"timestamp": "2026-07-10T00:00:02Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":     "main-thread",
				"cwd":    "/tmp",
				"source": "vscode",
			},
		},
	)

	s := New(Config{ClaudeDir: claudeDir, CodexDir: codexDir})
	sessions, err := s.scanSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "main-thread" || sessions[0].Path != mainSession {
		t.Fatalf("sessions = %#v", sessions)
	}

	// Default discovery hides auxiliary rollouts, while an explicitly opened
	// path remains available for targeted inspection.
	explicit := New(Config{ClaudeDir: claudeDir, CodexDir: codexDir, OpenSession: subagentSession})
	explicitSessions, err := explicit.listSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(explicitSessions) != 2 {
		t.Fatalf("explicit sessions = %#v", explicitSessions)
	}
	foundSubagent := false
	for _, session := range explicitSessions {
		if session.Path == subagentSession && session.ID == "child-thread" {
			foundSubagent = true
		}
	}
	if !foundSubagent {
		t.Fatalf("explicit subagent missing from %#v", explicitSessions)
	}
}

func TestRepoMapServesCitymapWithoutSession(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "a.go"), []byte("package demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "b.go"), []byte("package demo\n\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(Config{RepoRoot: repoRoot, MapOnly: true})
	resp := httptest.NewRecorder()
	s.handleRepoMap(resp, httptest.NewRequest(http.MethodGet, "/api/repomap", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("repomap status = %d body=%s", resp.Code, resp.Body.String())
	}
	var city model.CityMap
	if err := json.Unmarshal(resp.Body.Bytes(), &city); err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 2 || city.Repo.Root == "" {
		t.Fatalf("city = %#v", city)
	}

	// A second request returns the cached build.
	if _, err := s.repoCityMap(repoRoot); err != nil {
		t.Fatal(err)
	}
}

func TestRepoMapAcceptsRepoQueryParam(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "a.go"), []byte("package demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No RepoRoot configured; the repo comes entirely from the query param.
	s := New(Config{})
	resp := httptest.NewRecorder()
	s.handleRepoMap(resp, httptest.NewRequest(http.MethodGet, "/api/repomap?repo="+url.QueryEscape(repoRoot), nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("repomap status = %d body=%s", resp.Code, resp.Body.String())
	}
	var city model.CityMap
	if err := json.Unmarshal(resp.Body.Bytes(), &city); err != nil {
		t.Fatal(err)
	}
	if len(city.Files) != 1 {
		t.Fatalf("city = %#v", city)
	}
}

func TestRepoMapWithoutRepoRootReturns404(t *testing.T) {
	s := New(Config{})
	resp := httptest.NewRecorder()
	s.handleRepoMap(resp, httptest.NewRequest(http.MethodGet, "/api/repomap", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("repomap status = %d, want 404", resp.Code)
	}
}

func TestRepoMapCacheExpiresWhenRepoChanges(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "a.go"), []byte("package demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(Config{RepoRoot: repoRoot})
	first, err := s.repoCityMap(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != 1 {
		t.Fatalf("initial files = %d, want 1", len(first.Files))
	}

	// add a file, then age the cache entry past its TTL: the next build must
	// pick up the new file instead of returning the stale map
	if err := os.WriteFile(filepath.Join(repoRoot, "b.go"), []byte("package demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(repoRoot)
	s.repoMapMu.Lock()
	entry := s.repoMaps[abs]
	entry.builtAt = entry.builtAt.Add(-2 * repoMapTTL)
	s.repoMaps[abs] = entry
	s.repoMapMu.Unlock()

	second, err := s.repoCityMap(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Files) != 2 {
		t.Fatalf("files after change = %d, want 2 (stale cache returned)", len(second.Files))
	}
}

func TestRepoMapCacheIsBounded(t *testing.T) {
	s := New(Config{})
	for i := 0; i < repoMapMaxEntries+5; i++ {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package demo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.repoCityMap(repo); err != nil {
			t.Fatal(err)
		}
	}
	s.repoMapMu.Lock()
	n := len(s.repoMaps)
	s.repoMapMu.Unlock()
	if n > repoMapMaxEntries {
		t.Fatalf("repo map cache size = %d, want <= %d", n, repoMapMaxEntries)
	}
}

func writeServerSession(t *testing.T, path string, lines ...string) {
	t.Helper()
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendServerSession(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func requestSessions(t *testing.T, s *Server, target string) []model.SessionMeta {
	t.Helper()
	resp := httptest.NewRecorder()
	s.handleSessions(resp, httptest.NewRequest(http.MethodGet, target, nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("sessions status = %d body=%s", resp.Code, resp.Body.String())
	}
	var sessions []model.SessionMeta
	if err := json.Unmarshal(resp.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	return sessions
}

type blockingSource struct {
	dir     string
	root    string
	started chan struct{}
	release chan struct{}
	parses  atomic.Int32
}

func (s *blockingSource) Harness() string    { return "blocking" }
func (s *blockingSource) SessionDir() string { return s.dir }
func (s *blockingSource) ListSessions() ([]model.SessionMeta, error) {
	return nil, nil
}

func (s *blockingSource) Summarize(path string) (model.SessionMeta, error) {
	return model.SessionMeta{
		Key:     adapter.SessionKey(s.Harness(), path),
		ID:      "blocking",
		Harness: s.Harness(),
		Path:    path,
		Cwd:     s.root,
	}, nil
}

func (s *blockingSource) Parse(path string) (*model.Trace, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if s.parses.Add(1) == 1 {
		close(s.started)
		<-s.release
	}
	return &model.Trace{
		Version: 1,
		Session: model.TraceSession{
			ID:      "blocking",
			Harness: s.Harness(),
			Title:   strings.TrimSpace(string(content)),
			Cwd:     s.root,
			Path:    path,
		},
		Events: []model.Event{},
		Marks:  []model.Mark{},
	}, nil
}

type traceMapResult struct {
	trace *model.Trace
	city  *model.CityMap
	err   error
}

func newBlockingServer(t *testing.T) (*Server, *blockingSource, string) {
	t.Helper()
	dir := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(dir, "blocking.jsonl")
	writeServerSession(t, session, "v1")
	source := &blockingSource{dir: dir, root: root, started: make(chan struct{}), release: make(chan struct{})}
	s := New(Config{})
	s.adapters = []adapter.Source{source}
	return s, source, session
}

func quoteJSON(path string) string {
	return strconv.Quote(filepath.ToSlash(path))
}

func writeServerJSONL(t *testing.T, path string, values ...any) {
	t.Helper()
	content := ""
	for _, value := range values {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		content += string(b) + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
