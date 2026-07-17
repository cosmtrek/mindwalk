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
	"time"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/citymap"
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

// A loader that panics mid-load must still close the inflight done channel
// and drop the inflight entry. Before the fix, net/http's per-connection
// recover swallowed such a panic while the entry stayed registered, so every
// later snapshot/report request for that session blocked forever on <-done
// until the server was restarted.
func TestInflightLoadSurvivesPanickingLoader(t *testing.T) {
	s, source, _ := newBlockingServer(t)
	source.panicMsg = "loader bug"

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

	first := awaitTraceMapResult(t, owner, "owner request")
	if first.err == nil || !strings.Contains(first.err.Error(), "loader bug") {
		t.Fatalf("owner error = %v, want the recovered panic", first.err)
	}
	// The waiter either shared the failed load or, arriving after cleanup,
	// ran its own parse (which succeeds past the first). Either way it must
	// return instead of blocking on a leaked done channel.
	second := awaitTraceMapResult(t, waiter, "waiter request")
	if second.err != nil && !strings.Contains(second.err.Error(), "loader bug") {
		t.Fatalf("waiter error = %v, want nil or the recovered panic", second.err)
	}

	// The key must not stay wedged: a fresh request reloads and succeeds.
	trace, city, err := s.traceAndMap("blocking")
	if err != nil || trace == nil || city == nil {
		t.Fatalf("request after recovered panic: trace=%v city=%v err=%v", trace, city, err)
	}

	s.mu.Lock()
	leaked := len(s.inflight)
	s.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("inflight entries leaked: %d", leaked)
	}
}

func awaitTraceMapResult(t *testing.T, ch <-chan traceMapResult, what string) traceMapResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never returned; inflight done channel leaked", what)
		return traceMapResult{}
	}
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

func TestCodexAgentAPIsDoNotDeriveChildAcrossDuplicateRootIDs(t *testing.T) {
	claudeDir := t.TempDir()
	codexDir := t.TempDir()
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "main.go"), []byte("package demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootAPath := filepath.Join(codexDir, "root-a.jsonl")
	rootBPath := filepath.Join(codexDir, "root-b.jsonl")
	childBPath := filepath.Join(codexDir, "child-b.jsonl")
	rootMeta := func() map[string]any {
		return map[string]any{
			"type": "session_meta",
			"payload": map[string]any{
				"id":  "shared-root",
				"cwd": filepath.ToSlash(repoRoot),
			},
		}
	}
	writeServerJSONL(t, rootAPath, rootMeta())
	writeServerJSONL(t, rootBPath,
		rootMeta(),
		map[string]any{
			"type": "response_item",
			"payload": map[string]any{
				"type":      "function_call",
				"id":        "fc-child-b",
				"name":      "spawn_agent",
				"arguments": `{"message":"owned by root B"}`,
				"call_id":   "call-child-b",
			},
		},
		map[string]any{
			"type": "response_item",
			"payload": map[string]any{
				"type":    "function_call_output",
				"call_id": "call-child-b",
				"output":  `{"agent_id":"child-b"}`,
			},
		},
	)
	writeServerJSONL(t, childBPath, map[string]any{
		"type": "session_meta",
		"payload": map[string]any{
			"id":               "child-b",
			"session_id":       "shared-root",
			"parent_thread_id": "shared-root",
			"cwd":              filepath.ToSlash(repoRoot),
			"thread_source":    "subagent",
			"source": map[string]any{
				"subagent": map[string]any{
					"thread_spawn": map[string]any{
						"parent_thread_id": "shared-root",
						"depth":            1,
						"agent_nickname":   "Root B Child",
					},
				},
			},
		},
	})

	s := New(Config{ClaudeDir: claudeDir, CodexDir: codexDir})
	sessions := requestSessions(t, s, "/api/sessions")
	if len(sessions) != 2 {
		t.Fatalf("visible sessions = %#v, want duplicate-ID roots only", sessions)
	}
	rootAKey := adapter.SessionKey("codex", rootAPath)
	rootBKey := adapter.SessionKey("codex", rootBPath)
	childBKey := adapter.SessionKey("codex", childBPath)
	rootBNodeID := adapter.AgentNodeID("codex", rootBKey, "codex-agent:child-b")

	rootAGraphResp := requestSessionResource(t, s, http.MethodGet, "/api/sessions/"+rootAKey+"/agents")
	if rootAGraphResp.Code != http.StatusOK {
		t.Fatalf("root A graph status=%d body=%q", rootAGraphResp.Code, rootAGraphResp.Body.String())
	}
	var rootAGraph model.AgentGraph
	if err := json.Unmarshal(rootAGraphResp.Body.Bytes(), &rootAGraph); err != nil {
		t.Fatal(err)
	}
	if len(rootAGraph.Agents) != 1 {
		t.Fatalf("root A absorbed root B child: %#v", rootAGraph.Agents)
	}

	rootBGraphResp := requestSessionResource(t, s, http.MethodGet, "/api/sessions/"+rootBKey+"/agents")
	if rootBGraphResp.Code != http.StatusOK {
		t.Fatalf("root B graph status=%d body=%q", rootBGraphResp.Code, rootBGraphResp.Body.String())
	}
	var rootBGraph model.AgentGraph
	if err := json.Unmarshal(rootBGraphResp.Body.Bytes(), &rootBGraph); err != nil {
		t.Fatal(err)
	}
	if len(rootBGraph.Agents) != 2 || rootBGraph.Agents[1].ID != rootBNodeID || rootBGraph.Agents[1].LinkQuality != model.AgentLinkQualityExact {
		t.Fatalf("root B exact child = %#v", rootBGraph.Agents)
	}

	for _, nodeID := range []string{
		rootBNodeID,
		adapter.AgentNodeID("codex", rootAKey, "session:"+childBKey),
	} {
		traceResp := requestSessionResource(t, s, http.MethodGet, "/api/sessions/"+rootAKey+"/agents/"+nodeID+"/trace")
		if traceResp.Code != http.StatusNotFound || strings.TrimSpace(traceResp.Body.String()) != "agent not found" {
			t.Fatalf("root A node %q status=%d body=%q", nodeID, traceResp.Code, traceResp.Body.String())
		}
	}
}

func TestServerRetainsClaudeSubagentInCatalog(t *testing.T) {
	claudeDir := t.TempDir()
	session := filepath.Join(claudeDir, "root-id.jsonl")
	subagent := filepath.Join(claudeDir, "root-id", "subagents", "agent-child.jsonl")
	if err := os.MkdirAll(filepath.Dir(subagent), 0o755); err != nil {
		t.Fatal(err)
	}
	writeServerSession(t, session,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"root-id","cwd":"/tmp","message":{"role":"user","content":"hello"}}`,
	)
	writeServerSession(t, subagent,
		`{"type":"user","timestamp":"2026-07-09T00:00:01Z","sessionId":"root-id","agentId":"child","isSidechain":true,"cwd":"/tmp","message":{"role":"user","content":"internal"}}`,
	)

	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex")})
	sessions := requestSessions(t, s, "/api/sessions")
	if len(sessions) != 1 || sessions[0].ID != "root-id" {
		t.Fatalf("sessions = %#v", sessions)
	}
	childKey := adapter.SessionKey("claude-code", subagent)
	child, ok := s.sessionCatalog[childKey]
	if !ok || !child.Auxiliary || child.ID != "child" {
		t.Fatalf("catalog child = %#v, ok = %v", child, ok)
	}

	if err := os.Remove(subagent); err != nil {
		t.Fatal(err)
	}
	requestSessions(t, s, "/api/sessions?fresh=1")
	if _, ok := s.sessionCatalog[childKey]; ok {
		t.Fatalf("stale child retained in fresh catalog: %#v", s.sessionCatalog[childKey])
	}
}

func TestAgentAPIsAreRootScoped(t *testing.T) {
	s, source := newAgentAPIServer(t)

	sessions := requestSessions(t, s, "/api/sessions")
	if len(sessions) != 2 {
		t.Fatalf("visible sessions = %#v, want two roots", sessions)
	}
	for _, session := range sessions {
		if session.Auxiliary {
			t.Fatalf("auxiliary session leaked into visible list: %#v", session)
		}
	}

	graphResp := requestSessionResource(t, s, http.MethodGet, "/api/sessions/root-a/agents")
	if graphResp.Code != http.StatusOK {
		t.Fatalf("graph status = %d body=%q", graphResp.Code, graphResp.Body.String())
	}
	var graph model.AgentGraph
	if err := json.Unmarshal(graphResp.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	if graph.RootSessionKey != "root-a" || len(graph.Agents) != 5 || graph.Agents[0].ID != "main-a" {
		t.Fatalf("graph = %#v", graph)
	}

	otherGraph := requestSessionResource(t, s, http.MethodGet, "/api/sessions/root-b/agents")
	if otherGraph.Code != http.StatusOK {
		t.Fatalf("other graph status = %d body=%q", otherGraph.Code, otherGraph.Body.String())
	}

	mainResp := requestSessionResource(t, s, http.MethodGet, "/api/sessions/root-a/agents/main-a/trace")
	if mainResp.Code != http.StatusOK {
		t.Fatalf("main trace status = %d body=%q", mainResp.Code, mainResp.Body.String())
	}
	var mainTrace model.Trace
	if err := json.Unmarshal(mainResp.Body.Bytes(), &mainTrace); err != nil {
		t.Fatal(err)
	}
	if mainTrace.Session.ID != "root-a" {
		t.Fatalf("main trace session = %#v", mainTrace.Session)
	}

	childResp := requestSessionResource(t, s, http.MethodGet, "/api/sessions/root-a/agents/child-a/trace")
	if childResp.Code != http.StatusOK {
		t.Fatalf("child trace status = %d body=%q", childResp.Code, childResp.Body.String())
	}
	var childWire struct {
		Events []struct {
			Targets json.RawMessage `json:"targets"`
		} `json:"events"`
	}
	if err := json.Unmarshal(childResp.Body.Bytes(), &childWire); err != nil {
		t.Fatal(err)
	}
	if len(childWire.Events) != 2 || string(childWire.Events[1].Targets) != "[]" {
		t.Fatalf("empty child targets serialized as null: %s", childResp.Body.String())
	}
	var childTrace model.Trace
	if err := json.Unmarshal(childResp.Body.Bytes(), &childTrace); err != nil {
		t.Fatal(err)
	}
	if childTrace.Session.ID != "child-a" || childTrace.Stats.FilesInRepo != 2 {
		t.Fatalf("child trace = %#v", childTrace)
	}
	if len(childTrace.Events) != 2 || len(childTrace.Events[0].Targets) != 1 || childTrace.Events[0].Targets[0].FileID == nil {
		t.Fatalf("child target was not assigned against root city: %#v", childTrace.Events)
	}
	_, rootCity, err := s.traceAndMap("root-a")
	if err != nil {
		t.Fatal(err)
	}
	wantFileID := 0
	for _, file := range rootCity.Files {
		if file.Path == "b.go" {
			wantFileID = file.ID
		}
	}
	if got := *childTrace.Events[0].Targets[0].FileID; wantFileID == 0 || got != wantFileID {
		t.Fatalf("child file id = %d, want root city b.go id %d", got, wantFileID)
	}

	zeroResp := requestSessionResource(t, s, http.MethodGet, "/api/sessions/root-a/agents/zero-a/trace")
	if zeroResp.Code != http.StatusOK {
		t.Fatalf("zero-event child status = %d body=%q", zeroResp.Code, zeroResp.Body.String())
	}
	var zeroWire struct {
		Events json.RawMessage `json:"events"`
		Marks  json.RawMessage `json:"marks"`
	}
	if err := json.Unmarshal(zeroResp.Body.Bytes(), &zeroWire); err != nil {
		t.Fatal(err)
	}
	if string(zeroWire.Events) != "[]" || string(zeroWire.Marks) != "[]" {
		t.Fatalf("zero-event child slices serialized as null: %s", zeroResp.Body.String())
	}

	var childMeta model.SessionMeta
	for _, meta := range source.metas {
		if meta.ID == "child-a" {
			childMeta = meta
			break
		}
	}
	cachedChild, childCity, err := s.traceAndMapMeta(childMeta)
	if err != nil {
		t.Fatal(err)
	}
	wantCachedFileID := 0
	for _, file := range childCity.Files {
		if file.Path == "b.go" {
			wantCachedFileID = file.ID
		}
	}
	if got := cachedChild.Events[0].Targets[0].FileID; got == nil || *got != wantCachedFileID {
		t.Fatalf("cached child file id mutated by root projection: got=%v want=%d", got, wantCachedFileID)
	}
	if cachedChild.Events[1].Targets != nil || cachedChild.Events[1].Outside != nil {
		t.Fatalf("cached child empty slices were mutated: %#v", cachedChild.Events[1])
	}
	projected := traceAgainstCity(cachedChild, rootCity)
	if projected.Events[1].Targets == nil || projected.Events[1].Outside == nil {
		t.Fatalf("projected child empty slices are nil: %#v", projected.Events[1])
	}
	projected.Events[0].Targets[0].Path = "changed.go"
	if cachedChild.Events[0].Targets[0].Path != "b.go" {
		t.Fatalf("projected child shares target storage with cache: %#v", cachedChild.Events[0].Targets[0])
	}
	projected.Events[0].Targets[0].Lines[0][0] = 99
	if got := cachedChild.Events[0].Targets[0].Lines[0][0]; got != 4 {
		t.Fatalf("projected child shares target line storage with cache: got=%d want=4", got)
	}
	sourceChild := source.traces[filepath.Clean(childMeta.Path)]
	if got := sourceChild.Events[0].Targets[0].Lines[0][0]; got != 4 {
		t.Fatalf("projected child shares target line storage with source: got=%d want=4", got)
	}

	parsesBeforeSecondChild := source.parses["child-a"]
	secondChild := requestSessionResource(t, s, http.MethodGet, "/api/sessions/root-a/agents/child-a/trace")
	if secondChild.Code != http.StatusOK || source.parses["child-a"] != parsesBeforeSecondChild+1 {
		t.Fatalf("direct child parse status=%d parses=%d body=%q", secondChild.Code, source.parses["child-a"], secondChild.Body.String())
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		status int
		body   string
	}{
		{name: "missing trace", method: http.MethodGet, path: "/api/sessions/root-a/agents/missing-a/trace", status: http.StatusConflict, body: "agent trace unavailable: missing"},
		{name: "failed launch", method: http.MethodGet, path: "/api/sessions/root-a/agents/failed-a/trace", status: http.StatusConflict, body: "agent trace unavailable: unavailable"},
		{name: "unknown node", method: http.MethodGet, path: "/api/sessions/root-a/agents/unknown/trace", status: http.StatusNotFound, body: "agent not found"},
		{name: "cross root node", method: http.MethodGet, path: "/api/sessions/root-a/agents/child-b/trace", status: http.StatusNotFound, body: "agent not found"},
		{name: "graph method", method: http.MethodPost, path: "/api/sessions/root-a/agents", status: http.StatusMethodNotAllowed, body: "method not allowed"},
		{name: "trace method", method: http.MethodPost, path: "/api/sessions/root-a/agents/child-a/trace", status: http.StatusMethodNotAllowed, body: "method not allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := requestSessionResource(t, s, tc.method, tc.path)
			if resp.Code != tc.status || strings.TrimSpace(resp.Body.String()) != tc.body {
				t.Fatalf("status=%d body=%q", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestAuxiliarySessionCannotBeAgentRoot(t *testing.T) {
	s, _ := newAgentAPIServer(t)
	resp := requestSessionResource(t, s, http.MethodGet, "/api/sessions/child-a-key/agents")
	if resp.Code != http.StatusNotFound || strings.TrimSpace(resp.Body.String()) != "session not found" {
		t.Fatalf("status=%d body=%q", resp.Code, resp.Body.String())
	}
}

func TestChildAgentTraceReusesRootCityMap(t *testing.T) {
	s, _ := newAgentAPIServer(t)
	requestSessions(t, s, "/api/sessions")
	rootMeta := s.sessionCatalog["root-a"]

	var builtRoots []string
	buildCityMap := citymap.Builder{}.Build
	s.buildCityMap = func(repoRoot string, trace *model.Trace) (*model.CityMap, error) {
		builtRoots = append(builtRoots, repoRoot)
		return buildCityMap(repoRoot, trace)
	}

	resp := requestSessionResource(t, s, http.MethodGet, "/api/sessions/root-a/agents/child-a/trace")
	if resp.Code != http.StatusOK {
		t.Fatalf("child trace status = %d body=%q", resp.Code, resp.Body.String())
	}
	var trace model.Trace
	if err := json.Unmarshal(resp.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.Stats.FilesInRepo != 2 || len(trace.Events) == 0 || len(trace.Events[0].Targets) == 0 || trace.Events[0].Targets[0].FileID == nil {
		t.Fatalf("child trace was not projected against root city: %#v", trace)
	}
	if len(builtRoots) != 1 || builtRoots[0] != rootMeta.Cwd {
		t.Fatalf("city builds = %q, want only root %q", builtRoots, rootMeta.Cwd)
	}
}

func TestAgentGraphCacheReusesMatchingFingerprint(t *testing.T) {
	s, source := newAgentAPIServer(t)
	root := requestSessions(t, s, "/api/sessions")[0]

	first, err := s.agentGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.agentGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("matching graph fingerprint did not reuse the cached graph")
	}
	if got := source.graphBuilds.Load(); got != 1 {
		t.Fatalf("graph builds = %d, want 1", got)
	}
}

func TestAgentGraphInflightSharesEightConcurrentBuilds(t *testing.T) {
	s, source := newAgentAPIServer(t)
	root := requestSessions(t, s, "/api/sessions")[0]
	source.graphStarted = make(chan struct{})
	source.graphRelease = make(chan struct{})

	results := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := s.agentGraph(root)
			results <- err
		}()
	}
	<-source.graphStarted
	close(source.graphRelease)
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := source.graphBuilds.Load(); got != 1 {
		t.Fatalf("concurrent graph builds = %d, want 1", got)
	}
}

func TestAgentGraphCacheRebuildsWhenInputChanges(t *testing.T) {
	s, source := newAgentAPIServer(t)
	root := requestSessions(t, s, "/api/sessions")[0]

	if _, err := s.agentGraph(root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.agentGraph(root); err != nil {
		t.Fatal(err)
	}
	appendServerSession(t, root.Path, "changed")
	if _, err := s.agentGraph(root); err != nil {
		t.Fatal(err)
	}
	if got := source.graphBuilds.Load(); got != 2 {
		t.Fatalf("graph builds after input change = %d, want 2", got)
	}
}

func TestFreshScanInvalidatesAgentGraphCache(t *testing.T) {
	s, source := newAgentAPIServer(t)
	root := requestSessions(t, s, "/api/sessions")[0]

	if _, err := s.agentGraph(root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.agentGraph(root); err != nil {
		t.Fatal(err)
	}
	requestSessions(t, s, "/api/sessions?fresh=1")
	if _, err := s.agentGraph(root); err != nil {
		t.Fatal(err)
	}
	if got := source.graphBuilds.Load(); got != 2 {
		t.Fatalf("graph builds after fresh scan = %d, want 2", got)
	}
}

func TestFreshScanReloadsClaudeSidecarMetadata(t *testing.T) {
	claudeDir := t.TempDir()
	root := filepath.Join(claudeDir, "root-id.jsonl")
	child := filepath.Join(claudeDir, "root-id", "subagents", "agent-child.jsonl")
	if err := os.MkdirAll(filepath.Dir(child), 0o755); err != nil {
		t.Fatal(err)
	}
	writeServerSession(t, root,
		`{"type":"user","timestamp":"2026-07-09T00:00:00Z","sessionId":"root-id","cwd":"/tmp","message":{"role":"user","content":"hello"}}`,
	)
	writeServerSession(t, child,
		`{"type":"user","timestamp":"2026-07-09T00:00:01Z","sessionId":"root-id","agentId":"child","isSidechain":true,"cwd":"/tmp","message":{"role":"user","content":"internal"}}`,
	)

	s := New(Config{ClaudeDir: claudeDir, CodexDir: filepath.Join(t.TempDir(), "codex")})
	requestSessions(t, s, "/api/sessions")
	childKey := adapter.SessionKey("claude-code", child)
	if got := s.sessionCatalog[childKey].Agent.Role; got != "" {
		t.Fatalf("initial role = %q, want empty", got)
	}

	sidecar := strings.TrimSuffix(child, ".jsonl") + ".meta.json"
	if err := os.WriteFile(sidecar, []byte(`{"agentType":"Explore","description":"Inspect child","toolUseId":"call-child","spawnDepth":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	requestSessions(t, s, "/api/sessions?fresh=1")
	meta := s.sessionCatalog[childKey]
	if meta.Agent == nil || meta.Agent.Role != "Explore" || meta.Agent.Depth != 2 || meta.Agent.LaunchCallID != "call-child" {
		t.Fatalf("fresh child agent meta = %#v", meta.Agent)
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

	// Explicitly opening an auxiliary rollout keeps it available internally
	// without leaking it into the visible session list.
	explicit := New(Config{ClaudeDir: claudeDir, CodexDir: codexDir, OpenSession: subagentSession})
	explicitSessions, err := explicit.listSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(explicitSessions) != 1 || explicitSessions[0].ID != "main-thread" {
		t.Fatalf("explicit sessions = %#v", explicitSessions)
	}
	childKey := adapter.SessionKey("codex", subagentSession)
	if child, ok := explicit.sessionCatalog[childKey]; !ok || child.ID != "child-thread" || !child.Auxiliary {
		t.Fatalf("explicit catalog child = %#v, ok = %v", child, ok)
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

func requestSessionResource(t *testing.T, s *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	resp := httptest.NewRecorder()
	s.handleSessionResource(resp, httptest.NewRequest(method, target, nil))
	return resp
}

type agentAPISource struct {
	dir          string
	metas        map[string]model.SessionMeta
	traces       map[string]*model.Trace
	graphs       map[string]*model.AgentGraph
	parses       map[string]int
	graphBuilds  atomic.Int32
	graphStarted chan struct{}
	graphRelease chan struct{}
}

func (s *agentAPISource) Harness() string    { return "agent-api" }
func (s *agentAPISource) SessionDir() string { return s.dir }
func (s *agentAPISource) ListSessions() ([]model.SessionMeta, error) {
	return nil, nil
}

func (s *agentAPISource) Summarize(path string) (model.SessionMeta, error) {
	meta, ok := s.metas[filepath.Clean(path)]
	if !ok {
		return model.SessionMeta{}, os.ErrNotExist
	}
	return meta, nil
}

func (s *agentAPISource) Parse(path string) (*model.Trace, error) {
	trace, ok := s.traces[filepath.Clean(path)]
	if !ok {
		return nil, os.ErrNotExist
	}
	s.parses[trace.Session.ID]++
	clone := *trace
	clone.Events = append([]model.Event(nil), trace.Events...)
	for i := range clone.Events {
		clone.Events[i].Targets = append([]model.Target(nil), trace.Events[i].Targets...)
		clone.Events[i].Outside = append([]model.OutsideTouch(nil), trace.Events[i].Outside...)
	}
	clone.Marks = append([]model.Mark(nil), trace.Marks...)
	return &clone, nil
}

func (s *agentAPISource) BuildAgentGraph(root model.SessionMeta, _ []model.SessionMeta) (*model.AgentGraph, error) {
	build := s.graphBuilds.Add(1)
	if build == 1 && s.graphStarted != nil {
		close(s.graphStarted)
		<-s.graphRelease
	}
	graph, ok := s.graphs[root.Key]
	if !ok {
		return nil, os.ErrNotExist
	}
	clone := *graph
	clone.Agents = append([]model.AgentNode(nil), graph.Agents...)
	return &clone, nil
}

func (s *agentAPISource) AgentGraphInputs(root model.SessionMeta, _ []model.SessionMeta) ([]string, error) {
	return []string{root.Path}, nil
}

func newAgentAPIServer(t *testing.T) (*Server, *agentAPISource) {
	t.Helper()
	dir := t.TempDir()
	rootRepo := t.TempDir()
	childRepo := t.TempDir()
	otherRepo := t.TempDir()
	for path, content := range map[string]string{
		filepath.Join(rootRepo, "a.go"):  "package demo\n",
		filepath.Join(rootRepo, "b.go"):  "package demo\n\nfunc B() {}\n",
		filepath.Join(childRepo, "b.go"): "package demo\n",
		filepath.Join(otherRepo, "c.go"): "package other\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths := map[string]string{}
	for _, id := range []string{"root-a", "child-a", "child-zero", "root-b", "child-b"} {
		paths[id] = filepath.Join(dir, id+".jsonl")
		writeServerSession(t, paths[id], "{}")
	}
	metas := map[string]model.SessionMeta{
		filepath.Clean(paths["root-a"]): {
			Key: "root-a", ID: "root-a", Harness: "agent-api", Path: paths["root-a"], Cwd: rootRepo, EventCount: 1,
		},
		filepath.Clean(paths["child-a"]): {
			Key: "child-a-key", ID: "child-a", Harness: "agent-api", Path: paths["child-a"], Cwd: childRepo, EventCount: 2, Auxiliary: true,
		},
		filepath.Clean(paths["child-zero"]): {
			Key: "child-zero-key", ID: "child-zero", Harness: "agent-api", Path: paths["child-zero"], Cwd: childRepo, EventCount: 0, Auxiliary: true,
		},
		filepath.Clean(paths["root-b"]): {
			Key: "root-b", ID: "root-b", Harness: "agent-api", Path: paths["root-b"], Cwd: otherRepo, EventCount: 1,
		},
		filepath.Clean(paths["child-b"]): {
			Key: "child-b-key", ID: "child-b", Harness: "agent-api", Path: paths["child-b"], Cwd: otherRepo, EventCount: 1, Auxiliary: true,
		},
	}
	traces := map[string]*model.Trace{}
	for id, target := range map[string]string{
		"root-a": "a.go", "child-a": "b.go", "root-b": "c.go", "child-b": "c.go",
	} {
		meta := metas[filepath.Clean(paths[id])]
		events := []model.Event{{Seq: 0, Tool: "Read", Action: "read", Targets: []model.Target{{Path: target, Touch: "read"}}}}
		if id == "child-a" {
			events[0].Targets[0].Lines = [][2]int{{4, 8}}
			events = append(events, model.Event{Seq: 1, Tool: "Exec", Action: "exec", Targets: []model.Target{}, Outside: []model.OutsideTouch{}})
		}
		traces[filepath.Clean(paths[id])] = &model.Trace{
			Version: 1,
			Session: model.TraceSession{ID: id, Harness: "agent-api", Cwd: meta.Cwd, Path: meta.Path, EventCount: len(events)},
			Events:  events,
			Marks:   []model.Mark{},
			Stats:   model.ComputeStats(&model.Trace{Events: events}, 1, model.ObservabilityExact),
		}
	}
	zeroMeta := metas[filepath.Clean(paths["child-zero"])]
	traces[filepath.Clean(paths["child-zero"])] = &model.Trace{
		Version: 1,
		Session: model.TraceSession{ID: "child-zero", Harness: "agent-api", Cwd: zeroMeta.Cwd, Path: zeroMeta.Path, EventCount: 0},
		Events:  []model.Event{},
		Marks:   []model.Mark{},
		Stats:   model.ComputeStats(&model.Trace{Events: []model.Event{}}, 1, model.ObservabilityExact),
	}
	graphs := map[string]*model.AgentGraph{
		"root-a": {
			Version: model.AgentGraphVersion, RootSessionKey: "root-a",
			Agents: []model.AgentNode{
				{ID: "main-a", Kind: model.AgentKindMain, Label: "Main", Status: model.AgentStatusMain, TraceAvailability: model.TraceAvailabilityAvailable, TraceSessionKey: "root-a"},
				{ID: "child-a", ParentID: "main-a", Depth: 1, Kind: model.AgentKindSubagent, Label: "Child A", Status: model.AgentStatusLaunched, TraceAvailability: model.TraceAvailabilityAvailable, TraceSessionKey: "child-a-key"},
				{ID: "zero-a", ParentID: "main-a", Depth: 1, Kind: model.AgentKindSubagent, Label: "Zero", Status: model.AgentStatusLaunched, TraceAvailability: model.TraceAvailabilityAvailable, TraceSessionKey: "child-zero-key"},
				{ID: "missing-a", ParentID: "main-a", Depth: 1, Kind: model.AgentKindSubagent, Label: "Missing", Status: model.AgentStatusLaunched, TraceAvailability: model.TraceAvailabilityMissing},
				{ID: "failed-a", ParentID: "main-a", Depth: 1, Kind: model.AgentKindSubagent, Label: "Failed", Status: model.AgentStatusFailed, TraceAvailability: model.TraceAvailabilityUnavailable},
			},
		},
		"root-b": {
			Version: model.AgentGraphVersion, RootSessionKey: "root-b",
			Agents: []model.AgentNode{
				{ID: "main-b", Kind: model.AgentKindMain, Label: "Main", Status: model.AgentStatusMain, TraceAvailability: model.TraceAvailabilityAvailable, TraceSessionKey: "root-b"},
				{ID: "child-b", ParentID: "main-b", Depth: 1, Kind: model.AgentKindSubagent, Label: "Child B", Status: model.AgentStatusLaunched, TraceAvailability: model.TraceAvailabilityAvailable, TraceSessionKey: "child-b-key"},
			},
		},
	}
	source := &agentAPISource{dir: dir, metas: metas, traces: traces, graphs: graphs, parses: map[string]int{}}
	s := New(Config{})
	s.adapters = []adapter.Source{source}
	return s, source
}

type blockingSource struct {
	dir     string
	root    string
	started chan struct{}
	release chan struct{}
	parses  atomic.Int32
	// panicMsg makes the first parse panic after release instead of
	// returning, mimicking a loader bug mid-load.
	panicMsg string
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
		if s.panicMsg != "" {
			panic(s.panicMsg)
		}
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
