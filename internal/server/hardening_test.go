package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestHandlerRejectsNonLocalHost(t *testing.T) {
	s := New(Config{})
	handler := s.handler()

	// A stray bracket must not normalize into an allowed host.
	for _, host := range []string{"evil.example:8765", "localhost]", "localhost]:8765", "[localhost:8765"} {
		req := httptest.NewRequest(http.MethodGet, "/api/repomap", nil)
		req.Host = host
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("Host %q status = %d, want 403", host, resp.Code)
		}
	}

	// Host names are case-insensitive.
	for _, host := range []string{"127.0.0.1:8765", "localhost:8765", "LOCALHOST:8765", "[::1]:8765"} {
		req := httptest.NewRequest(http.MethodGet, "/api/repomap", nil)
		req.Host = host
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code == http.StatusForbidden {
			t.Fatalf("local Host %q rejected", host)
		}
	}
}

func TestAnalyzeRejectsCrossSiteRequests(t *testing.T) {
	s := New(Config{})
	handler := s.handler()
	post := func(origin, fetchSite string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/nope/analyze", nil)
		req.Host = "127.0.0.1:8765"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp.Code
	}

	rejected := []struct{ origin, fetchSite string }{
		{"https://evil.example", ""},
		{"null", ""},
		{"", "cross-site"},
		{"", "same-site"},
		{"http://127.0.0.1:8765", "cross-site"},
		// Loopback is not same-origin: another local server's page — a
		// different host name, port, or scheme — must not pass.
		{"http://localhost:9999", ""},
		{"http://127.0.0.1:9999", ""},
		{"http://localhost:8765", ""},
		{"https://127.0.0.1:8765", ""},
	}
	for _, c := range rejected {
		if code := post(c.origin, c.fetchSite); code != http.StatusForbidden {
			t.Fatalf("origin=%q fetchSite=%q status = %d, want 403", c.origin, c.fetchSite, code)
		}
	}

	// Same-origin browser requests and non-browser clients (no Origin) must
	// pass the middleware; 404 here means the handler ran and found no session.
	allowed := []struct{ origin, fetchSite string }{
		{"", ""},
		{"http://127.0.0.1:8765", ""},
		{"http://127.0.0.1:8765", "same-origin"},
		{"", "none"},
	}
	for _, c := range allowed {
		if code := post(c.origin, c.fetchSite); code == http.StatusForbidden {
			t.Fatalf("origin=%q fetchSite=%q rejected by middleware", c.origin, c.fetchSite)
		}
	}
}

// Cross-site GETs stay readable-by-middleware (CORS already blocks the
// response); only non-GET methods carry the Origin gate.
func TestCrossSiteGetPassesMiddleware(t *testing.T) {
	s := New(Config{})
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Host = "127.0.0.1:8765"
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp := httptest.NewRecorder()
	s.handler().ServeHTTP(resp, req)
	if resp.Code == http.StatusForbidden {
		t.Fatal("cross-site GET rejected; reads are CORS-protected, not middleware-gated")
	}
}

func TestRepoMapUsesInjectedBuilder(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "a.go"), []byte("package demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(Config{})
	calls := 0
	s.buildCityMap = func(repo string, trace *model.Trace) (*model.CityMap, error) {
		calls++
		return emptyCityMap(repo), nil
	}
	if _, err := s.repoCityMap(repoRoot); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("injected builder calls = %d, want 1", calls)
	}
}

func TestAgentGraphCacheIsBounded(t *testing.T) {
	s := New(Config{})
	s.mu.Lock()
	base := time.Now()
	for i := 0; i < agentGraphMaxEntries+5; i++ {
		s.agentGraphs[string(rune('a'+i))] = agentGraphCacheEntry{
			graph: &model.AgentGraph{},
			used:  base.Add(time.Duration(i) * time.Second),
		}
	}
	s.evictAgentGraphsLocked()
	n := len(s.agentGraphs)
	_, newestKept := s.agentGraphs[string(rune('a'+agentGraphMaxEntries+4))]
	_, oldestKept := s.agentGraphs["a"]
	s.mu.Unlock()
	if n != agentGraphMaxEntries {
		t.Fatalf("agent graph cache size = %d, want %d", n, agentGraphMaxEntries)
	}
	if !newestKept || oldestKept {
		t.Fatalf("eviction order wrong: newestKept=%v oldestKept=%v", newestKept, oldestKept)
	}
}
