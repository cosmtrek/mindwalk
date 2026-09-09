package server

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/adapter/claudecode"
	"github.com/cosmtrek/mindwalk/internal/adapter/codex"
	"github.com/cosmtrek/mindwalk/internal/adapter/crush"
	"github.com/cosmtrek/mindwalk/internal/adapter/pi"
	"github.com/cosmtrek/mindwalk/internal/citymap"
	"github.com/cosmtrek/mindwalk/internal/judge"
	"github.com/cosmtrek/mindwalk/internal/model"
)

//go:embed static
var embeddedStatic embed.FS

type Config struct {
	Port      int
	ClaudeDir string
	CodexDir  string
	PiDir     string
	CrushDir  string
	// DisableCrush skips registering the Crush adapter entirely.
	// Useful for projects where the .crush directory does not
	// belong to the user (e.g. vendored test fixtures) or for
	// users who never want the Crush scan to run.
	DisableCrush bool
	OpenSession  string
	Dev          bool
	RepoRoot     string
	MapOnly      bool
}

type Server struct {
	cfg             Config
	adapters        []adapter.Source
	mu              sync.Mutex
	scanMu          sync.Mutex
	sessions        []model.SessionMeta
	sessionCatalog  map[string]model.SessionMeta
	sessionAt       time.Time
	freshGen        uint64
	traceStore      *traceStore
	agentGraphs     map[string]agentGraphCacheEntry
	agentGraphLoads map[string]*inflightAgentGraph
	summaries       map[string]summaryCacheEntry
	repoMaps        map[string]repoMapEntry
	repoMapMu       sync.Mutex
	buildCityMap    func(string, *model.Trace) (*model.CityMap, error)

	analyze     analyzeState
	reportCache judge.Cache
	reportIndex reportIndex

	// httpServer is set inside Start so Shutdown can gracefully
	// cancel in-flight SSE handlers when the CLI receives SIGTERM.
	httpServer *http.Server
}

type repoMapEntry struct {
	city    *model.CityMap
	builtAt time.Time
}

type fileFingerprint struct {
	size    int64
	modTime time.Time
}

type agentGraphFingerprint struct {
	digest   [sha256.Size]byte
	freshGen uint64
}

type agentGraphCacheEntry struct {
	fingerprint agentGraphFingerprint
	graph       *model.AgentGraph
	used        time.Time
}

type inflightAgentGraph struct {
	done        chan struct{}
	fingerprint agentGraphFingerprint
	graph       *model.AgentGraph
	err         error
}

type summaryCacheEntry struct {
	size    int64
	modTime time.Time
	// sidecar is opaque validation material for the companion files the
	// source declared via SummaryInputs; empty when there are none.
	sidecar string
	meta    model.SessionMeta
}

const (
	sessionListTTL       = 5 * time.Second
	traceCacheTTL        = 10 * time.Minute
	traceCacheMaxEntries = 16
	// repo map builds are relatively cheap; a short TTL keeps a long-running
	// serve current as the tree changes without rebuilding on every request
	repoMapTTL        = 30 * time.Second
	repoMapMaxEntries = 16
	// agent graphs are small but were the one unbounded cache; bound them the
	// same way as traces
	agentGraphMaxEntries = 16
	// readHeaderTimeout caps how long a slow client can hold a
	// connection open while sending headers. 10s is enough for a
	// real browser over loopback but breaks Slowloris-style
	// attacks (gosec G112).
	readHeaderTimeout = 10 * time.Second
)

func New(cfg Config) *Server {
	s := &Server{
		cfg:             cfg,
		adapters:        buildAdapters(cfg),
		agentGraphs:     map[string]agentGraphCacheEntry{},
		agentGraphLoads: map[string]*inflightAgentGraph{},
		summaries:       map[string]summaryCacheEntry{},
		sessionCatalog:  map[string]model.SessionMeta{},
		repoMaps:        map[string]repoMapEntry{},
		buildCityMap:    citymap.Builder{}.Build,

		analyze:     analyzeState{jobs: map[string]*analyzeJob{}},
		reportCache: judge.Cache{Dir: judge.DefaultCacheDir()},
	}
	// Method values, not results: the store observes buildCityMap overrides
	// made after construction (tests swap it per case).
	s.traceStore = newTraceStore(s.loadTraceAndMap, s.parseSessionTrace)
	return s
}

// Close releases resources held by adapters (database handles,
// file handles). Adapters that do not implement adapter.Closer are
// skipped. Safe to call multiple times.
func (s *Server) Close() error {
	var firstErr error
	for _, src := range s.adapters {
		if c, ok := src.(adapter.Closer); ok {
			if err := c.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (s *Server) Start(openBrowser bool) error {
	port := s.cfg.Port
	if port == 0 {
		port = 0
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	addr := "http://" + ln.Addr().String()
	// warm the session scan so the first page load doesn't wait on a cold walk
	// over every session file. Map-only mode never lists sessions, so skip the
	// scan of the whole Claude/Codex corpus.
	if !s.cfg.MapOnly {
		go func() { _, _ = s.listSessions() }()
	}
	if openBrowser {
		pageURL := addr
		switch {
		case s.cfg.MapOnly:
			pageURL += "/?map=1"
		case s.cfg.OpenSession != "":
			pageURL += "/?session=" + url.QueryEscape(s.openSessionKey())
		}
		_ = openURL(pageURL)
	}
	fmt.Printf("mindwalk serving %s\n", addr)
	// Use http.Server (not http.Serve) so callers can call Shutdown
	// for graceful SSE teardown — http.Serve blocks until the
	// listener closes, which never cancels in-flight SSE handlers.
	// ReadHeaderTimeout protects against Slowloris-style attacks
	// (gosec G112); the server only serves loopback traffic by
	// default, but the explicit cap is cheap insurance.
	s.httpServer = &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the server started by Start, cancelling
// in-flight SSE handlers within the supplied context. Safe to call
// before Start (no-op).
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/sessions/", s.handleSessionResource)
	mux.HandleFunc("/api/adapters", s.handleAdapters)
	mux.HandleFunc("/api/repomap", s.handleRepoMap)
	mux.HandleFunc("/", s.handleStatic)
	return requireLoopback(mux)
}

// requireLoopback rejects requests that did not originate locally. Binding to
// 127.0.0.1 alone does not keep browsers out: any web page can fire
// cross-site requests straight at a loopback port, and DNS rebinding can
// point an attacker-controlled name at 127.0.0.1. The Host check pins the
// name rebinding would forge; the Origin / Sec-Fetch-Site check stops
// cross-site state changes — POST /analyze starts a judge run that costs
// tokens and minutes. An Origin must name this server exactly: another
// loopback origin (a different local server's page, or another port) is
// still cross-origin. Requests without an Origin header (curl, same-origin
// navigations) pass untouched.
func requireLoopback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "forbidden: non-local Host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
				http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
				return
			}
			switch r.Header.Get("Sec-Fetch-Site") {
			case "", "same-origin", "none":
			default:
				http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = normalizeHost(host)
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// sameOrigin reports whether the Origin header names this server: scheme
// http (the server never terminates TLS) and the same normalized host:port
// the request was addressed to. "null" and malformed origins fail parsing
// and are rejected.
func sameOrigin(origin, requestHost string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return false
	}
	return hostPortKey(parsed.Host) == hostPortKey(requestHost)
}

// hostPortKey normalizes a host:port for origin comparison: lowercased host,
// IPv6 brackets stripped, the http default port made explicit.
func hostPortKey(hostport string) string {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, "80"
	}
	return normalizeHost(host) + ":" + port
}

// normalizeHost lowercases a host and strips one matched pair of IPv6
// brackets. Unmatched brackets stay put, so a malformed host ("localhost]")
// remains malformed instead of normalizing into an allowed value.
func normalizeHost(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return strings.ToLower(host)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessions, err := s.listSessionsFresh(r.URL.Query().Get("fresh") == "1")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// annotate each session with its evaluation state so the rail can show
	// running/finished badges without per-session report requests
	items := make([]sessionListItem, len(sessions))
	for i, meta := range sessions {
		items[i] = sessionListItem{SessionMeta: meta, ReportState: s.reportStateFor(meta)}
	}
	writeJSON(w, items)
}

type sessionListItem struct {
	model.SessionMeta
	ReportState string `json:"reportState,omitempty"`
}

type adapterInfo struct {
	Harness      string `json:"harness"`
	SessionDir   string `json:"sessionDir"`
	SessionCount int    `json:"sessionCount"`
	AgentGraph   bool   `json:"agentGraph"`
}

// handleAdapters lists the registered session sources with their session
// counts so the UI (and `mindwalk doctor`) can show what mindwalk sees.
func (s *Server) handleAdapters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	counts := make(map[string]int, len(s.adapters))
	for _, sess := range s.sessions {
		counts[sess.Harness]++
	}
	s.mu.Unlock()
	infos := make([]adapterInfo, 0, len(s.adapters))
	for _, src := range s.adapters {
		infos = append(infos, adapterInfo{
			Harness:      src.Harness(),
			SessionDir:   src.SessionDir(),
			SessionCount: counts[src.Harness()],
			AgentGraph:   adapter.IsAgentGraphSource(src),
		})
	}
	writeJSON(w, infos)
}

func (s *Server) handleSessionResource(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/")
	if len(parts) == 2 && parts[1] == "agents" {
		s.handleSessionAgents(w, r, parts[0])
		return
	}
	if len(parts) == 4 && parts[1] == "agents" && parts[3] == "trace" {
		s.handleSessionAgentTrace(w, r, parts[0], parts[2])
		return
	}
	if len(parts) == 3 && parts[1] == "analyze" && parts[2] == "stream" {
		s.handleSessionAnalyzeStream(w, r, parts[0])
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	selector, resource := parts[0], parts[1]
	switch resource {
	case "snapshot":
		trace, city, err := s.traceAndMap(selector)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, struct {
			Trace *model.Trace   `json:"trace"`
			City  *model.CityMap `json:"city"`
		}{Trace: trace, City: city})
	case "trace":
		trace, _, err := s.traceAndMap(selector)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, trace)
	case "citymap":
		_, city, err := s.traceAndMap(selector)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, city)
	case "report":
		s.handleSessionReport(w, r, selector)
	case "analyze":
		s.handleSessionAnalyze(w, r, selector)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleSessionAgents(w http.ResponseWriter, r *http.Request, selector string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	root, err := s.findSession(selector)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	graph, err := s.agentGraph(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, graph)
}

func (s *Server) handleSessionAgentTrace(w http.ResponseWriter, r *http.Request, selector, nodeID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	root, err := s.findSession(selector)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	graph, err := s.agentGraph(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var node *model.AgentNode
	for i := range graph.Agents {
		if graph.Agents[i].ID == nodeID {
			node = &graph.Agents[i]
			break
		}
	}
	if node == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if node.TraceAvailability != model.TraceAvailabilityAvailable {
		http.Error(w, "agent trace unavailable: "+node.TraceAvailability, http.StatusConflict)
		return
	}

	if node.Kind == model.AgentKindMain {
		trace, _, err := s.traceAndMapMeta(root)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, trace)
		return
	}
	child, err := s.findCatalogSession(node.TraceSessionKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, rootCity, err := s.traceAndMapMeta(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The raw layer caches the unprojected parse; projecting onto the root
	// city clones, so the cached trace is never mutated.
	trace, err := s.traceStore.LoadRaw(child)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, traceAgainstCity(trace, rootCity))
}

// handleRepoMap serves the citymap for a repo with no session / trace attached.
// It backs the static full-repo map view (mindwalk map <repo> and the ?map=1 UI
// mode). The repo path comes from the ?repo= query param, falling back to the
// server's configured RepoRoot. Maps are cached per path with a short TTL so a
// long-running serve picks up tree changes, and the cache is size-bounded.
//
// The path is trusted: the server is localhost-only and already builds citymaps
// for arbitrary session repos, so accepting a repo path here does not widen the
// read surface. The builder only reads the tree (git ls-files / walk).
func (s *Server) handleRepoMap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		repo = s.cfg.RepoRoot
	}
	if repo == "" {
		http.Error(w, "no repo configured", http.StatusNotFound)
		return
	}
	city, err := s.repoCityMap(repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, city)
}

func (s *Server) repoCityMap(repo string) (*model.CityMap, error) {
	if abs, err := filepath.Abs(repo); err == nil {
		repo = abs
	}
	s.repoMapMu.Lock()
	defer s.repoMapMu.Unlock()
	if entry, ok := s.repoMaps[repo]; ok && time.Since(entry.builtAt) < repoMapTTL {
		return entry.city, nil
	}
	city, err := s.buildCityMap(repo, nil)
	if err != nil {
		return nil, err
	}
	s.repoMaps[repo] = repoMapEntry{city: city, builtAt: time.Now()}
	s.evictRepoMapsLocked()
	return city, nil
}

// evictRepoMapsLocked bounds the repo-map cache by dropping the oldest entries
// once it grows past repoMapMaxEntries. Caller must hold repoMapMu.
func (s *Server) evictRepoMapsLocked() {
	for len(s.repoMaps) > repoMapMaxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range s.repoMaps {
			if oldestKey == "" || entry.builtAt.Before(oldest) {
				oldestKey = key
				oldest = entry.builtAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(s.repoMaps, oldestKey)
	}
}

func (s *Server) listSessions() ([]model.SessionMeta, error) {
	return s.listSessionsFresh(false)
}

func (s *Server) listSessionsFresh(fresh bool) ([]model.SessionMeta, error) {
	s.mu.Lock()
	observedFreshGen := s.freshGen
	s.mu.Unlock()
	return s.listSessionsObserved(fresh, observedFreshGen)
}

func (s *Server) listSessionsObserved(fresh bool, observedFreshGen uint64) ([]model.SessionMeta, error) {
	// scanMu serializes scans so callers arriving mid-scan wait for the
	// in-flight result instead of duplicating the walk
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.mu.Lock()
	if s.sessions != nil && ((!fresh && time.Since(s.sessionAt) < sessionListTTL) || (fresh && s.freshGen != observedFreshGen)) {
		sessions := append([]model.SessionMeta(nil), s.sessions...)
		s.mu.Unlock()
		return sessions, nil
	}
	s.mu.Unlock()

	sessions, err := s.scanSessions()
	if err != nil {
		return nil, err
	}
	if s.cfg.OpenSession != "" {
		meta, err := s.summarizeAnyCached(s.cfg.OpenSession, nil)
		if err == nil {
			s.mu.Lock()
			s.sessionCatalog[meta.Key] = meta
			s.mu.Unlock()
			if !meta.Auxiliary {
				found := false
				for i := range sessions {
					if sessions[i].Key == meta.Key {
						sessions[i] = meta
						found = true
						break
					}
				}
				if !found {
					sessions = append([]model.SessionMeta{meta}, sessions...)
				}
			}
		}
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].EndedAt > sessions[j].EndedAt
	})
	s.mu.Lock()
	s.sessions = sessions
	s.sessionAt = time.Now()
	if fresh {
		s.freshGen++
		clear(s.agentGraphs)
	}
	s.mu.Unlock()
	return sessions, nil
}

func (s *Server) scanSessions() ([]model.SessionMeta, error) {
	type sessionFile struct {
		source adapter.Source
		path   string
		info   fs.FileInfo
	}
	seen := map[string]bool{}
	var files []sessionFile
	counts := map[string]int{}
	for _, source := range s.adapters {
		// Most harnesses store sessions as files on disk. The
		// Crush adapter uses a SQLite database instead, so we
		// additionally ask it for its enumerated metadata and
		// short-circuit before the directory walk. Each Crush
		// path is synthetic (crush://...) so the legacy WalkDir
		// would not find it; we record it here and let the
		// summarise pass below pick it up.
		metas, err := source.ListSessions()
		if err != nil {
			return nil, fmt.Errorf("%s sessions: %w", source.Harness(), err)
		}
		counts[source.Harness()] = len(metas)
		if !sourceUsesFilesystem(metas) {
			for _, meta := range metas {
				seen[summaryKey(source, meta.Path)] = true
				files = append(files, sessionFile{source: source, path: meta.Path})
			}
			continue
		}
		dir := source.SessionDir()
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".jsonl" {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			seen[summaryKey(source, path)] = true
			files = append(files, sessionFile{source: source, path: path, info: info})
			counts[source.Harness()]++
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// summarizing reads every uncached session file; spread the parsing
	// across cores so a cold scan doesn't serialize gigabytes of JSONL
	results := make([]*model.SessionMeta, len(files))
	workers := runtime.NumCPU()
	if workers > len(files) {
		workers = len(files)
	}
	if workers > 1 {
		jobs := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					if meta, err := s.summarizeCached(files[i].source, files[i].path, files[i].info); err == nil {
						results[i] = &meta
					}
				}
			}()
		}
		for i := range files {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
	} else {
		for i := range files {
			if meta, err := s.summarizeCached(files[i].source, files[i].path, files[i].info); err == nil {
				results[i] = &meta
			}
		}
	}

	catalog := make(map[string]model.SessionMeta, len(files))
	sessions := make([]model.SessionMeta, 0, len(files))
	for _, meta := range results {
		if meta == nil {
			continue
		}
		catalog[meta.Key] = *meta
		if !meta.Auxiliary {
			sessions = append(sessions, *meta)
		}
	}
	s.mu.Lock()
	s.sessionCatalog = catalog
	s.mu.Unlock()
	s.pruneSummaryCache(seen)
	log.Printf("mindwalk: found %d session(s) — %s", len(sessions), formatAdapterCounts(counts))
	return sessions, nil
}

// formatAdapterCounts renders a per-harness session count summary
// for the log line emitted by scanSessions. Output order is
// deterministic so the line is stable across cold scans.
func formatAdapterCounts(counts map[string]int) string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%d %s", counts[name], name))
	}
	return strings.Join(parts, ", ")
}

// sourceUsesFilesystem reports whether a list of metas came from a
// filesystem walk rather than an explicit adapter enumeration. The
// Crush adapter returns synthetic paths rooted at
// "crush://session/" so the presence of a single such path is a
// strong signal the adapter is database-backed and the legacy
// directory walk should be skipped.
func sourceUsesFilesystem(metas []model.SessionMeta) bool {
	for _, meta := range metas {
		if crush.IsSessionPath(meta.Path) {
			return false
		}
	}
	return true
}

func (s *Server) summarizeAnyCached(path string, info fs.FileInfo) (model.SessionMeta, error) {
	var lastErr error
	for _, source := range s.adapters {
		meta, err := s.summarizeCached(source, path, info)
		if err == nil {
			return meta, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return model.SessionMeta{}, lastErr
	}
	return model.SessionMeta{}, errors.New("no session adapters configured")
}

func (s *Server) summarizeCached(source adapter.Source, path string, info fs.FileInfo) (model.SessionMeta, error) {
	if info == nil {
		// Adapters that store sessions outside the filesystem
		// (e.g. Crush's SQLite database) surface a synthetic path
		// that os.Stat will reject. Fall through to a direct
		// adapter call instead of running the fingerprint short
		// circuit and sidecar file checks that only make sense
		// for real on-disk files.
		meta, err := source.Summarize(path)
		if err != nil {
			return model.SessionMeta{}, err
		}
		if meta.Key == "" {
			meta.Key = adapter.SessionKey(source.Harness(), path)
		}
		return meta, nil
	}
	key := summaryKey(source, path)
	sidecar := summarySidecarDigest(source, path)
	s.mu.Lock()
	if cached, ok := s.summaries[key]; ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) &&
		cached.sidecar == sidecar {
		meta := cached.meta
		s.mu.Unlock()
		return meta, nil
	}
	s.mu.Unlock()

	meta, err := source.Summarize(path)
	if err != nil {
		return model.SessionMeta{}, err
	}
	if meta.Key == "" {
		meta.Key = adapter.SessionKey(source.Harness(), path)
	}
	s.mu.Lock()
	s.summaries[key] = summaryCacheEntry{
		size:    info.Size(),
		modTime: info.ModTime(),
		sidecar: sidecar,
		meta:    meta,
	}
	s.mu.Unlock()
	return meta, nil
}

// summarySidecarDigest fingerprints the companion files the source declares
// for path. The server holds no harness-specific sidecar knowledge — sources
// that keep files beside their session logs implement SummarySidecarSource.
func summarySidecarDigest(source adapter.Source, path string) string {
	sidecars, ok := source.(adapter.SummarySidecarSource)
	if !ok {
		return ""
	}
	inputs := sidecars.SummaryInputs(path)
	if len(inputs) == 0 {
		return ""
	}
	var material strings.Builder
	for _, input := range inputs {
		if fingerprint, err := fingerprintFile(input); err == nil {
			fmt.Fprintf(&material, "%s\x00%d\x00%d\n", input, fingerprint.size, fingerprint.modTime.UnixNano())
		} else {
			fmt.Fprintf(&material, "%s\x00missing\n", input)
		}
	}
	return material.String()
}

func (s *Server) pruneSummaryCache(seen map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.summaries {
		if !seen[key] && summaryPath(key) != s.cfg.OpenSession {
			delete(s.summaries, key)
		}
	}
}

func (s *Server) traceAndMap(selector string) (*model.Trace, *model.CityMap, error) {
	meta, err := s.findSession(selector)
	if err != nil {
		return nil, nil, err
	}
	return s.traceAndMapMeta(meta)
}

func (s *Server) traceAndMapMeta(meta model.SessionMeta) (*model.Trace, *model.CityMap, error) {
	return s.traceStore.LoadSnapshot(meta)
}

func (s *Server) agentGraph(root model.SessionMeta) (*model.AgentGraph, error) {
	source := s.adapterForHarness(root.Harness)
	graphSource, ok := source.(adapter.AgentGraphSource)
	if !ok {
		// A source without subagent support is not a server failure: answer
		// with the same single-root graph a graph-capable source returns for
		// a session that launched no subagents.
		return &model.AgentGraph{
			Version:        model.AgentGraphVersion,
			RootSessionKey: root.Key,
			Agents: []model.AgentNode{{
				ID:                adapter.AgentNodeID(root.Harness, root.Key, "root:"+root.Key),
				Depth:             0,
				Kind:              model.AgentKindMain,
				Label:             "Main",
				Status:            model.AgentStatusMain,
				TraceAvailability: model.TraceAvailabilityAvailable,
				TraceSessionKey:   root.Key,
				TraceEventCount:   root.EventCount,
				LinkQuality:       model.AgentLinkQualityExact,
				LinkMethod:        model.AgentLinkMethodRoot,
			}},
		}, nil
	}
	for {
		s.mu.Lock()
		catalog := make([]model.SessionMeta, 0, len(s.sessionCatalog))
		for _, session := range s.sessionCatalog {
			catalog = append(catalog, session)
		}
		freshGen := s.freshGen
		s.mu.Unlock()
		sort.Slice(catalog, func(i, j int) bool { return catalog[i].Key < catalog[j].Key })

		inputs, err := graphSource.AgentGraphInputs(root, catalog)
		if err != nil {
			return nil, err
		}
		fingerprint, err := fingerprintAgentGraphInputs(inputs, freshGen)
		if err != nil {
			return nil, err
		}

		s.mu.Lock()
		if cached, ok := s.agentGraphs[root.Key]; ok && cached.fingerprint == fingerprint {
			cached.used = time.Now()
			s.agentGraphs[root.Key] = cached
			s.mu.Unlock()
			return cached.graph, nil
		}
		if load := s.agentGraphLoads[root.Key]; load != nil {
			done := load.done
			shareSnapshot := load.fingerprint == fingerprint
			s.mu.Unlock()
			<-done
			if shareSnapshot {
				return load.graph, load.err
			}
			continue
		}
		load := &inflightAgentGraph{done: make(chan struct{}), fingerprint: fingerprint}
		s.agentGraphLoads[root.Key] = load
		s.mu.Unlock()

		// On a memory miss, try the disk cache before paying for
		// a fresh BuildAgentGraph call. The digest is stable across
		// restarts so this warms cold starts.
		if graph, ok := loadAgentGraphFromDisk(fingerprint.digest); ok {
			s.mu.Lock()
			s.agentGraphs[root.Key] = agentGraphCacheEntry{fingerprint: fingerprint, graph: graph}
			if s.agentGraphLoads[root.Key] == load {
				delete(s.agentGraphLoads, root.Key)
			}
			close(load.done)
			s.mu.Unlock()
			return graph, nil
		}

		s.runAgentGraphInflight(root.Key, load, graphSource, root, catalog)
		return load.graph, load.err
	}
}

func fingerprintAgentGraphInputs(paths []string, freshGen uint64) (agentGraphFingerprint, error) {
	diskDigest, err := agentGraphDiskDigest(paths)
	if err != nil {
		return agentGraphFingerprint{}, err
	}
	return agentGraphFingerprint{digest: diskDigest, freshGen: freshGen}, nil
}

// agentGraphDiskDigest hashes the database file paths, sizes, and
// modification times into a stable key that survives server restarts.
// Synthetic crush:// paths are hashed by their string form. The
// returned digest is the disk cache key for a graph built from the
// given inputs.
func agentGraphDiskDigest(paths []string) ([sha256.Size]byte, error) {
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	var material strings.Builder
	previous := ""
	for _, path := range paths {
		path = filepath.Clean(path)
		if path == previous {
			continue
		}
		previous = path
		if crush.IsSessionPath(path) {
			fmt.Fprintf(&material, "%s\x00synthetic\n", path)
			continue
		}
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(&material, "%s\x00missing\n", path)
			continue
		}
		if err != nil {
			return [sha256.Size]byte{}, err
		}
		fmt.Fprintf(&material, "%s\x00%d\x00%d\n", path, info.Size(), info.ModTime().UnixNano())
	}
	return sha256.Sum256([]byte(material.String())), nil
}

func (s *Server) runAgentGraphInflight(key string, load *inflightAgentGraph, source adapter.AgentGraphSource, root model.SessionMeta, catalog []model.SessionMeta) {
	var (
		storeDigest [sha256.Size]byte
		storeGraph  *model.AgentGraph
	)
	defer func() {
		if r := recover(); r != nil {
			load.graph = nil
			load.err = fmt.Errorf("build agent graph %s: %v", key, r)
			log.Printf("mindwalk: panic building agent graph %s: %v\n%s", key, r, debug.Stack())
		}
		s.mu.Lock()
		if load.err == nil {
			s.agentGraphs[key] = agentGraphCacheEntry{fingerprint: load.fingerprint, graph: load.graph, used: time.Now()}
			s.evictAgentGraphsLocked()
			// Capture the snapshot for the disk write, then drop the
			// lock before performing I/O. Holding mu across the write
			// would block every concurrent agent-graph request behind
			// the slowest disk — load.graph is read-only after the
			// assignment above, so the snapshot is safe to publish.
			storeDigest = load.fingerprint.digest
			storeGraph = load.graph
		}
		if s.agentGraphLoads[key] == load {
			delete(s.agentGraphLoads, key)
		}
		close(load.done)
		s.mu.Unlock()

		if storeGraph != nil {
			storeAgentGraphToDisk(storeDigest, storeGraph)
		}
	}()
	load.graph, load.err = source.BuildAgentGraph(root, catalog)
}

// evictAgentGraphsLocked bounds the graph cache by dropping the least
// recently used entries. Caller must hold mu.
func (s *Server) evictAgentGraphsLocked() {
	for len(s.agentGraphs) > agentGraphMaxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range s.agentGraphs {
			if oldestKey == "" || entry.used.Before(oldest) {
				oldestKey = key
				oldest = entry.used
			}
		}
		if oldestKey == "" {
			return
		}
		delete(s.agentGraphs, oldestKey)
	}
}

// agentGraphCacheVersion is the on-disk format version. Bumping this
// invalidates all existing cache files so a format change never serves
// a stale or incompatible graph.
const agentGraphCacheVersion = 1

// agentGraphCacheFile wraps a persisted graph with a version tag so
// future format changes can reject old files cleanly.
type agentGraphCacheFile struct {
	Version int              `json:"version"`
	Graph   model.AgentGraph `json:"graph"`
}

// maxAgentGraphCacheBytes is the soft cap for the total disk cache.
// When exceeded, oldest files are evicted until usage drops below it.
const maxAgentGraphCacheBytes = 100 * 1024 * 1024 // 100 MB

// agentGraphCacheDir returns the disk directory for persisted agent
// graphs. Created lazily on first store. When MINDWALK_HOME is set
// (e.g. to a temp dir in tests), the cache lives there instead.
func agentGraphCacheDir() string {
	home := adapter.MindwalkHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "agent-graphs")
}

// loadAgentGraphFromDisk tries to read a previously persisted agent
// graph for the given digest. Returns (nil, false) on any miss,
// version mismatch, or corruption.
func loadAgentGraphFromDisk(digest [sha256.Size]byte) (*model.AgentGraph, bool) {
	dir := agentGraphCacheDir()
	if dir == "" {
		return nil, false
	}
	path := filepath.Join(dir, hex.EncodeToString(digest[:])+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var file agentGraphCacheFile
	if err := json.Unmarshal(data, &file); err != nil {
		log.Printf("mindwalk: agent-graph disk cache: corrupt file %s: %v", path, err)
		return nil, false
	}
	if file.Version != agentGraphCacheVersion {
		log.Printf("mindwalk: agent-graph disk cache: version %d in %s (expected %d), ignoring",
			file.Version, path, agentGraphCacheVersion)
		return nil, false
	}
	log.Printf("mindwalk: agent-graph disk cache hit: %s", path)
	return &file.Graph, true
}

// storeAgentGraphToDisk writes the graph to disk keyed by digest so
// the next cold start can skip rebuilding it. Errors are logged but
// never block the caller — the disk cache is best-effort.
func storeAgentGraphToDisk(digest [sha256.Size]byte, graph *model.AgentGraph) {
	dir := agentGraphCacheDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("mindwalk: agent-graph disk cache: cannot create dir %s: %v", dir, err)
		return
	}
	file := agentGraphCacheFile{Version: agentGraphCacheVersion, Graph: *graph}
	data, err := json.Marshal(file)
	if err != nil {
		log.Printf("mindwalk: agent-graph disk cache: marshal error: %v", err)
		return
	}
	path := filepath.Join(dir, hex.EncodeToString(digest[:])+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("mindwalk: agent-graph disk cache: write %s: %v", path, err)
		return
	}
	log.Printf("mindwalk: agent-graph disk cache miss: stored %s", path)
	evictAgentGraphCache(dir)
}

// evictAgentGraphCache removes oldest cache files when the total size
// of the directory exceeds maxAgentGraphCacheBytes. Files are sorted
// by modification time (oldest first) and deleted until usage is under
// the cap.
func evictAgentGraphCache(dir string) {
	evictAgentGraphCacheN(dir, maxAgentGraphCacheBytes)
}

// evictAgentGraphCacheN is the size-parameterised core of
// evictAgentGraphCache, extracted so tests can exercise eviction with a
// small threshold instead of writing 100 MB of fixture files.
func evictAgentGraphCacheN(dir string, maxBytes int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type cacheFile struct {
		path    string
		size    int64
		modTime int64
	}
	var files []cacheFile
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, cacheFile{
			path:    filepath.Join(dir, entry.Name()),
			size:    info.Size(),
			modTime: info.ModTime().UnixNano(),
		})
		total += info.Size()
	}
	if total <= maxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime < files[j].modTime
	})
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
			log.Printf("mindwalk: agent-graph disk cache: evicted %s", f.path)
		}
	}
}

func (s *Server) findCatalogSession(key string) (model.SessionMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, ok := s.sessionCatalog[key]
	if !ok {
		return model.SessionMeta{}, errors.New("session not found")
	}
	return meta, nil
}

func (s *Server) loadTraceAndMap(meta model.SessionMeta) (*model.Trace, *model.CityMap, error) {
	trace, err := s.parseSessionTrace(meta)
	if err != nil {
		return nil, nil, err
	}
	repoRoot := trace.Session.Cwd
	if repoRoot == "" {
		repoRoot = meta.Cwd
	}
	if repoRoot == "" {
		repoRoot = s.cfg.RepoRoot
	}
	if repoRoot == "" && !crush.IsSessionPath(meta.Path) {
		repoRoot = filepath.Dir(meta.Path)
	}
	city, err := s.buildCityMap(repoRoot, trace)
	if err != nil {
		log.Printf("mindwalk: citymap build failed for %s: %v; serving empty map", repoRoot, err)
		city = emptyCityMap(repoRoot)
	} else {
		assignFileIDs(trace, city)
	}
	// Recompute with the citymap's file count, carrying over the adapter's
	// grade for its error signal — the recount cannot re-derive it.
	trace.Stats = model.ComputeStats(trace, repoFileCount(city), model.ObservabilitySignals{Errors: trace.Stats.Observability.Errors})
	return trace, city, nil
}

func (s *Server) parseSessionTrace(meta model.SessionMeta) (*model.Trace, error) {
	source := s.adapterForHarness(meta.Harness)
	if source == nil {
		return nil, fmt.Errorf("no adapter for harness %q", meta.Harness)
	}
	trace, parseErr := source.Parse(meta.Path)
	if trace == nil {
		if parseErr != nil {
			return nil, parseErr
		}
		return nil, errors.New("trace unavailable")
	}
	return trace, nil
}

func emptyCityMap(repoRoot string) *model.CityMap {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		root = repoRoot
	}
	return &model.CityMap{
		Version: 1,
		Repo: model.RepoMeta{
			Root:        root,
			Dirty:       false,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Files: []model.CityFile{},
		Dirs:  []model.CityDir{},
		Layout: model.LayoutMeta{
			Algorithm: "unavailable",
			Weight:    "none",
		},
	}
}

func repoFileCount(city *model.CityMap) int {
	count := 0
	for _, file := range city.Files {
		if !file.Ghost {
			count++
		}
	}
	return count
}

func (s *Server) findSession(selector string) (model.SessionMeta, error) {
	sessions, err := s.listSessions()
	if err != nil {
		return model.SessionMeta{}, err
	}
	for _, session := range sessions {
		if session.Key == selector {
			return session, nil
		}
	}
	var matches []model.SessionMeta
	for _, session := range sessions {
		basename := strings.TrimSuffix(filepath.Base(session.Path), filepath.Ext(session.Path))
		if session.ID == selector || basename == selector {
			matches = append(matches, session)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return model.SessionMeta{}, fmt.Errorf("session selector %q is ambiguous; use the session key", selector)
	}
	return model.SessionMeta{}, errors.New("session not found")
}

func fingerprintFile(path string) (fileFingerprint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileFingerprint{}, err
	}
	return fileFingerprint{size: info.Size(), modTime: info.ModTime()}, nil
}

// fingerprintPath handles paths that may not be real on-disk files.
// Adapters that surface sessions via a database (Crush) hand the
// rest of the server a synthetic "crush://session/<id>" handle;
// os.Stat rejects those, but the trace cache still needs a
// fingerprint key for every session. We synthesise a stable zero
// fingerprint for those paths so the cache always misses — every
// request is delegated to the adapter, which can answer from its
// own in-memory or DB layer. The choice is deliberate: a hash of
// session metadata would let two different sessions masquerade as
// one if their paths happened to collide, so the trade is "always
// hit the source" instead.
func fingerprintPath(path string) (fileFingerprint, error) {
	if crush.IsSessionPath(path) {
		return fileFingerprint{}, nil
	}
	return fingerprintFile(path)
}

func (f fileFingerprint) equal(other fileFingerprint) bool {
	return f.size == other.size && f.modTime.Equal(other.modTime)
}

func (s *Server) openSessionKey() string {
	key := strings.TrimSuffix(filepath.Base(s.cfg.OpenSession), filepath.Ext(s.cfg.OpenSession))
	if meta, err := s.summarizeAnyCached(s.cfg.OpenSession, nil); err == nil && meta.Key != "" {
		key = meta.Key
	}
	return key
}

func (s *Server) adapterForHarness(harness string) adapter.Source {
	for _, source := range s.adapters {
		if source.Harness() == harness {
			return source
		}
	}
	return nil
}

func summaryKey(source adapter.Source, path string) string {
	return source.Harness() + "\x00" + path
}

func summaryPath(key string) string {
	if idx := strings.IndexByte(key, 0); idx >= 0 {
		return key[idx+1:]
	}
	return key
}

func assignFileIDs(trace *model.Trace, city *model.CityMap) {
	ids := map[string]int{}
	for _, file := range city.Files {
		ids[file.Path] = file.ID
	}
	for ei := range trace.Events {
		for ti := range trace.Events[ei].Targets {
			trace.Events[ei].Targets[ti].FileID = nil
			if id, ok := ids[trace.Events[ei].Targets[ti].Path]; ok {
				v := id
				trace.Events[ei].Targets[ti].FileID = &v
			}
		}
	}
}

func traceAgainstCity(trace *model.Trace, city *model.CityMap) *model.Trace {
	clone := *trace
	clone.Events = append([]model.Event{}, trace.Events...)
	for i := range clone.Events {
		clone.Events[i].Targets = append([]model.Target{}, trace.Events[i].Targets...)
		for j := range clone.Events[i].Targets {
			clone.Events[i].Targets[j].Lines = append([][2]int{}, trace.Events[i].Targets[j].Lines...)
		}
		clone.Events[i].Outside = append([]model.OutsideTouch{}, trace.Events[i].Outside...)
	}
	clone.Marks = append([]model.Mark{}, trace.Marks...)
	assignFileIDs(&clone, city)
	clone.Stats = model.ComputeStats(&clone, repoFileCount(city), model.ObservabilitySignals{Errors: trace.Stats.Observability.Errors})
	return &clone
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	if s.cfg.Dev && s.serveDist(w, r) {
		return
	}
	static, _ := fs.Sub(embeddedStatic, "static")
	http.FileServer(http.FS(static)).ServeHTTP(w, r)
}

func (s *Server) serveDist(w http.ResponseWriter, r *http.Request) bool {
	candidates := []string{
		filepath.Join("web", "dist"),
		filepath.Join("..", "web", "dist"),
	}
	for _, root := range candidates {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		full := filepath.Join(root, filepath.Clean(path))
		if !strings.HasPrefix(full, filepath.Clean(root)) {
			http.Error(w, "bad path", http.StatusBadRequest)
			return true
		}
		if info, err := os.Stat(full); err != nil || info.IsDir() {
			full = filepath.Join(root, "index.html")
		}
		if ext := filepath.Ext(full); ext != "" {
			if typ := mime.TypeByExtension(ext); typ != "" {
				w.Header().Set("Content-Type", typ)
			}
		}
		http.ServeFile(w, r, full)
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// buildAdapters assembles the registered adapter sources. The Crush
// adapter can be disabled via Config.DisableCrush (the --no-crush flag).
func buildAdapters(cfg Config) []adapter.Source {
	sources := []adapter.Source{
		claudecode.Adapter{Dir: cfg.ClaudeDir},
		codex.Adapter{Dir: cfg.CodexDir},
		pi.Adapter{Dir: cfg.PiDir},
	}
	if cfg.DisableCrush {
		return sources
	}
	return append(sources, crush.NewAdapter(cfg.CrushDir))
}

func openURL(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
