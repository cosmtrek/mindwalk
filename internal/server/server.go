package server

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/adapter/claudecode"
	"github.com/cosmtrek/mindwalk/internal/adapter/codex"
	"github.com/cosmtrek/mindwalk/internal/agents"
	"github.com/cosmtrek/mindwalk/internal/brain"
	"github.com/cosmtrek/mindwalk/internal/citymap"
	"github.com/cosmtrek/mindwalk/internal/event"
	"github.com/cosmtrek/mindwalk/internal/ingest"
	"github.com/cosmtrek/mindwalk/internal/integration"
	"github.com/cosmtrek/mindwalk/internal/model"
	"github.com/cosmtrek/mindwalk/internal/product"
	"github.com/cosmtrek/mindwalk/internal/redact"
	"github.com/cosmtrek/mindwalk/internal/registry"
	"github.com/cosmtrek/mindwalk/internal/review"
)

//go:embed static
var embeddedStatic embed.FS

type Config struct {
	Port         int
	ClaudeDir    string
	CodexDir     string
	OpenSession  string
	Dev          bool
	RepoRoot     string
	MapOnly      bool
	RegistryPath string
	// DataRoot contains the append-only observable-event ledger, resumable
	// tail state, and metadata-only quarantine. Empty disables live ingestion
	// for narrow open/map/test servers.
	DataRoot string
	// RegistryOnly limits normal Observatory session listing to enabled,
	// explicitly registered repository roots. Explicit `mindwalk open` keeps
	// the upstream single-session behavior by leaving this false.
	RegistryOnly bool
}

type Server struct {
	cfg             Config
	adapters        []adapter.Source
	mu              sync.Mutex
	scanMu          sync.Mutex
	sessions        []model.SessionMeta
	sessionAt       time.Time
	freshGen        uint64
	traces          map[string]*model.Trace
	maps            map[string]*model.CityMap
	cacheAt         map[string]time.Time
	cacheUsed       map[string]time.Time
	cacheFile       map[string]fileFingerprint
	inflight        map[string]*inflightLoad
	summaries       map[string]summaryCacheEntry
	repoMaps        map[string]repoMapEntry
	repoMapMu       sync.Mutex
	registryMu      sync.Mutex
	ingestion       *ingest.Manager
	ingestionErr    error
	brain           *brain.Store
	brainErr        error
	discovery       *discoveryManager
	streamPoll      time.Duration
	streamHeartbeat time.Duration
	streamBatch     int
}

type repoMapEntry struct {
	city    *model.CityMap
	builtAt time.Time
}

type inflightLoad struct {
	done        chan struct{}
	fingerprint fileFingerprint
	trace       *model.Trace
	city        *model.CityMap
	err         error
}

type fileFingerprint struct {
	size    int64
	modTime time.Time
}

type summaryCacheEntry struct {
	size    int64
	modTime time.Time
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
)

func New(cfg Config) *Server {
	if cfg.RegistryPath == "" {
		cfg.RegistryPath, _ = registry.DefaultPath(product.DirName)
	}
	s := &Server{
		cfg:             cfg,
		adapters:        []adapter.Source{claudecode.Adapter{Dir: cfg.ClaudeDir}, codex.Adapter{Dir: cfg.CodexDir}},
		traces:          map[string]*model.Trace{},
		maps:            map[string]*model.CityMap{},
		cacheAt:         map[string]time.Time{},
		cacheUsed:       map[string]time.Time{},
		cacheFile:       map[string]fileFingerprint{},
		inflight:        map[string]*inflightLoad{},
		summaries:       map[string]summaryCacheEntry{},
		repoMaps:        map[string]repoMapEntry{},
		streamPoll:      750 * time.Millisecond,
		streamHeartbeat: 15 * time.Second,
		streamBatch:     256,
	}
	if cfg.DataRoot != "" {
		s.ingestion, s.ingestionErr = ingest.NewManager(ingest.ManagerConfig{
			Sources:      s.adapters,
			RegistryPath: cfg.RegistryPath,
			DataRoot:     cfg.DataRoot,
		})
		s.brain, s.brainErr = brain.Open(filepath.Join(cfg.DataRoot, "brain"))
	}
	s.discovery = newDiscoveryManager(cfg.RegistryPath, cfg.DataRoot)
	return s
}

// DefaultDataRoot returns the local per-user directory for durable
// Observatory state. Session sources and repository contents never live here.
func DefaultDataRoot() (string, error) {
	if base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); base != "" {
		return filepath.Join(base, product.DirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", product.DirName), nil
}

func (s *Server) Start(openBrowser bool) error {
	stopIngestion := make(chan struct{})
	if s.ingestion != nil {
		_ = s.ingestion.PollAll()
		go s.pollIngestion(stopIngestion)
		defer func() {
			close(stopIngestion)
			_ = s.ingestion.Close()
		}()
	}
	mux := s.Handler()

	port := s.cfg.Port
	if port == 0 {
		port = 0
	}
	ln, err := net.Listen("tcp", listenAddress(port))
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
	return http.Serve(ln, mux)
}

func listenAddress(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }

func (s *Server) pollIngestion(stop <-chan struct{}) {
	ticker := time.NewTicker(s.streamPoll)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			_ = s.ingestion.PollAll()
		}
	}
}

// Handler returns the complete localhost application handler. Keeping route
// construction separate from Start makes API and security behavior testable
// without opening a real listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/sessions/", s.handleSessionResource)
	mux.HandleFunc("/api/sources", s.handleSources)
	mux.HandleFunc("/api/ingestion/health", s.handleIngestionHealth)
	mux.HandleFunc("/api/ingestion/sessions", s.handleIngestionSessions)
	mux.HandleFunc("/api/quarantine", s.handleQuarantine)
	mux.HandleFunc("/api/events/", s.handleEventResource)
	mux.HandleFunc("/api/memories/search", s.handleMemorySearch)
	mux.HandleFunc("/api/memories", s.handleMemories)
	mux.HandleFunc("/api/memories/", s.handleMemoryResource)
	mux.HandleFunc("/api/compare", s.handleComparison)
	mux.HandleFunc("/api/integrations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, integration.Catalog())
	})
	mux.HandleFunc("/api/repomap", s.handleRepoMap)
	mux.HandleFunc("/api/repositories", s.handleRepositories)
	mux.HandleFunc("/api/repositories/", s.handleRepositoryResource)
	mux.HandleFunc("/api/repository-discovery/config", s.handleDiscoveryConfig)
	mux.HandleFunc("/api/repository-discovery/start", s.handleDiscoveryStart)
	mux.HandleFunc("/api/repository-discovery/status", s.handleDiscoveryStatus)
	mux.HandleFunc("/api/repository-discovery/cancel", s.handleDiscoveryCancel)
	mux.HandleFunc("/api/repository-discovery/results", s.handleDiscoveryResults)
	mux.HandleFunc("/api/repository-discovery/hide", s.handleDiscoveryHide)
	mux.HandleFunc("/api/repository-discovery/register", s.handleDiscoveryRegister)
	mux.HandleFunc("/api/repository-discovery/forget", s.handleDiscoveryForget)
	mux.HandleFunc("/api/repository-discovery/reset-exclusions", s.handleDiscoveryResetExclusions)
	mux.HandleFunc("/", s.handleStatic)
	return securityHeaders(mux)
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
	writeJSON(w, sessions)
}

func (s *Server) handleSessionResource(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/")
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
	case "stream":
		s.handleSessionStream(w, r, selector)
	case "events":
		s.handleSessionEvents(w, r, selector)
	case "projection":
		s.handleSessionProjection(w, r, selector)
	case "agents":
		s.handleSessionAgents(w, r, selector)
	case "review":
		s.handleSessionReview(w, r, selector)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleSessionStream(w http.ResponseWriter, r *http.Request, selector string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	meta, err := s.findSession(selector)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if s.ingestion == nil {
		http.Error(w, "durable live ingestion is unavailable", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	cursor, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	poll := time.NewTicker(s.streamPoll)
	heartbeat := time.NewTicker(s.streamHeartbeat)
	defer poll.Stop()
	defer heartbeat.Stop()

	sendEvents := func() bool {
		items, latest, truncated, err := s.ingestion.EventsAfter(cursor, meta.Key, s.streamBatch)
		if err != nil {
			fmt.Fprint(w, "event: status\ndata: {\"status\":\"disconnected\"}\n\n")
			flusher.Flush()
			return false
		}
		for _, item := range items {
			data, err := json.Marshal(item)
			if err != nil {
				return false
			}
			fmt.Fprintf(w, "id: %d\nevent: observable\ndata: %s\n\n", item.Sequence, data)
			cursor = item.Sequence
		}
		if truncated {
			fmt.Fprintf(w, "event: status\ndata: {\"status\":\"replay-bounded\",\"nextSequence\":%d}\n\n", cursor)
			flusher.Flush()
			return false
		}
		if latest > cursor {
			fmt.Fprintf(w, "id: %d\nevent: checkpoint\ndata: {\"sequence\":%d}\n\n", latest, latest)
			cursor = latest
		}
		flusher.Flush()
		return true
	}
	if !sendEvents() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
			if !sendEvents() {
				return
			}
		case now := <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat %s\n\n", now.UTC().Format(time.RFC3339))
			flusher.Flush()
		}
	}
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	if !s.requireIngestionGET(w, r) {
		return
	}
	writeJSON(w, s.ingestion.Sources())
}

func (s *Server) handleIngestionHealth(w http.ResponseWriter, r *http.Request) {
	if !s.requireIngestionGET(w, r) {
		return
	}
	writeJSON(w, s.ingestion.Health())
}

func (s *Server) handleIngestionSessions(w http.ResponseWriter, r *http.Request) {
	if !s.requireIngestionGET(w, r) {
		return
	}
	writeJSON(w, s.ingestion.Sessions())
}

func (s *Server) handleQuarantine(w http.ResponseWriter, r *http.Request) {
	if !s.requireIngestionGET(w, r) {
		return
	}
	health := s.ingestion.Health()
	writeJSON(w, struct {
		Count int64 `json:"count"`
	}{Count: health.QuarantineCount})
}

func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request, selector string) {
	if !s.requireIngestionGET(w, r) {
		return
	}
	meta, err := s.findSession(selector)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 256
	}
	items, latest, truncated, err := s.ingestion.EventsAfter(after, meta.Key, limit)
	if err != nil {
		http.Error(w, "event ledger unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, struct {
		Events    []ingest.StreamEvent `json:"events"`
		Latest    int64                `json:"latestSequence"`
		Truncated bool                 `json:"truncated"`
	}{Events: items, Latest: latest, Truncated: truncated})
}

func (s *Server) handleSessionProjection(w http.ResponseWriter, r *http.Request, selector string) {
	if !s.requireIngestionGET(w, r) {
		return
	}
	meta, err := s.findSession(selector)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	projection, err := s.ingestion.Projection(meta.Key)
	if err != nil {
		http.Error(w, "projection unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, projection)
}

func (s *Server) handleSessionAgents(w http.ResponseWriter, r *http.Request, selector string) {
	if !s.requireIngestionGET(w, r) {
		return
	}
	meta, err := s.findSession(selector)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	processes, err := s.ingestion.AgentProcesses(meta.Key)
	if err != nil {
		http.Error(w, "agent projection unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, processes)
}

func (s *Server) handleSessionReview(w http.ResponseWriter, r *http.Request, selector string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	meta, err := s.findSession(selector)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	trace, _, err := s.traceAndMap(meta.Key)
	if err != nil {
		http.Error(w, "session trace unavailable", http.StatusNotFound)
		return
	}
	var processes []agents.Process
	if s.ingestion != nil {
		processes, _ = s.ingestion.AgentProcesses(meta.Key)
	}
	packet := review.Analyze(trace, processes)
	if r.URL.Query().Get("format") == "markdown" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="mindwalk-review.md"`)
		_, _ = w.Write([]byte(review.Markdown(packet)))
		return
	}
	writeJSON(w, packet)
}

func (s *Server) handleComparison(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	leftMeta, err := s.findSession(r.URL.Query().Get("left"))
	if err != nil {
		http.Error(w, "left session not found", http.StatusNotFound)
		return
	}
	rightMeta, err := s.findSession(r.URL.Query().Get("right"))
	if err != nil {
		http.Error(w, "right session not found", http.StatusNotFound)
		return
	}
	left, _, err := s.traceAndMap(leftMeta.Key)
	if err != nil {
		http.Error(w, "left session trace unavailable", http.StatusNotFound)
		return
	}
	right, _, err := s.traceAndMap(rightMeta.Key)
	if err != nil {
		http.Error(w, "right session trace unavailable", http.StatusNotFound)
		return
	}
	var leftAgents, rightAgents []agents.Process
	if s.ingestion != nil {
		leftAgents, _ = s.ingestion.AgentProcesses(leftMeta.Key)
		rightAgents, _ = s.ingestion.AgentProcesses(rightMeta.Key)
	}
	writeJSON(w, review.Compare(left, right, leftAgents, rightAgents))
}

func (s *Server) handleEventResource(w http.ResponseWriter, r *http.Request) {
	if !s.requireIngestionGET(w, r) {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/events/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "provenance" {
		http.NotFound(w, r)
		return
	}
	events, err := s.ingestion.Events("")
	if err != nil {
		http.Error(w, "event ledger unavailable", http.StatusInternalServerError)
		return
	}
	for _, envelope := range events {
		if envelope.EventID == parts[0] {
			writeJSON(w, struct {
				EventID    string           `json:"eventId"`
				EventType  string           `json:"eventType"`
				Provenance event.Provenance `json:"provenance"`
				Redactions []string         `json:"redactedFields,omitempty"`
			}{EventID: envelope.EventID, EventType: envelope.EventType, Provenance: envelope.Provenance, Redactions: envelope.RedactedFields})
			return
		}
	}
	http.Error(w, "event not found", http.StatusNotFound)
}

func (s *Server) requireIngestionGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if s.ingestionErr != nil || s.ingestion == nil {
		http.Error(w, "durable live ingestion is unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
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
	repo := s.cfg.RepoRoot
	requested := r.URL.Query().Get("repo")
	if requested != "" && repo != "" {
		requestedAbs, _ := filepath.Abs(requested)
		rootAbs, _ := filepath.Abs(repo)
		if requestedAbs != rootAbs {
			http.Error(w, "repository path is not the configured map root", http.StatusForbidden)
			return
		}
		repo = requestedAbs
	} else if requested != "" {
		http.Error(w, "arbitrary repository paths are disabled; use a registered repository", http.StatusForbidden)
		return
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

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; font-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
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
	city, err := citymap.Builder{}.Build(repo, nil)
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
		// Preserve an empty JSON array for the browser contract. Appending to a
		// nil slice turns a cached empty result into JSON null, which makes the
		// first-run UI treat sessions as a non-array and crash.
		sessions := append([]model.SessionMeta{}, s.sessions...)
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
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].EndedAt > sessions[j].EndedAt
	})
	s.mu.Lock()
	s.sessions = sessions
	s.sessionAt = time.Now()
	if fresh {
		s.freshGen++
	}
	s.mu.Unlock()
	return sessions, nil
}

func (s *Server) scanSessions() ([]model.SessionMeta, error) {
	allowed, err := s.allowedRepositoryRoots()
	if err != nil {
		return nil, err
	}
	if s.cfg.RegistryOnly && len(allowed) == 0 {
		return []model.SessionMeta{}, nil
	}
	type sessionFile struct {
		source adapter.Source
		path   string
		info   fs.FileInfo
	}
	seen := map[string]bool{}
	var files []sessionFile
	for _, source := range s.adapters {
		dir := source.SessionDir()
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".jsonl" || skipSessionFile(source, path) {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			seen[summaryKey(source, path)] = true
			files = append(files, sessionFile{source: source, path: path, info: info})
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

	sessions := make([]model.SessionMeta, 0, len(files))
	for _, meta := range results {
		if meta != nil && !meta.Auxiliary && (!s.cfg.RegistryOnly || withinAllowedRoot(meta.Cwd, allowed)) {
			sessions = append(sessions, *meta)
		}
	}
	s.pruneSummaryCache(seen)
	return sessions, nil
}

func (s *Server) allowedRepositoryRoots() ([]string, error) {
	if !s.cfg.RegistryOnly {
		return nil, nil
	}
	s.registryMu.Lock()
	defer s.registryMu.Unlock()
	reg, err := registry.Load(s.cfg.RegistryPath)
	if err != nil {
		return nil, err
	}
	var roots []string
	for _, repo := range reg.List() {
		if !repo.Enabled {
			continue
		}
		status, err := reg.StatusOf(repo.ID)
		if err == nil && !status.Missing && !status.InvalidPath {
			roots = append(roots, repo.Path)
		}
	}
	return roots, nil
}

func withinAllowedRoot(path string, roots []string) bool {
	if path == "" {
		return false
	}
	for _, root := range roots {
		if registry.Within(root, path) {
			return true
		}
	}
	return false
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
		var err error
		info, err = os.Stat(path)
		if err != nil {
			return model.SessionMeta{}, err
		}
	}
	key := summaryKey(source, path)
	s.mu.Lock()
	if cached, ok := s.summaries[key]; ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
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
	redact.SessionMeta(&meta)
	s.mu.Lock()
	s.summaries[key] = summaryCacheEntry{size: info.Size(), modTime: info.ModTime(), meta: meta}
	s.mu.Unlock()
	return meta, nil
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
	key := meta.Key
	if key == "" {
		key = adapter.SessionKey(meta.Harness, meta.Path)
	}
	for {
		fingerprint, err := fingerprintFile(meta.Path)
		if err != nil {
			s.mu.Lock()
			s.deleteTraceCacheLocked(key)
			s.mu.Unlock()
			return nil, nil, err
		}

		s.mu.Lock()
		if trace := s.traces[key]; trace != nil {
			cachedFingerprint, versioned := s.cacheFile[key]
			if versioned && cachedFingerprint.equal(fingerprint) && time.Since(s.cacheAt[key]) < traceCacheTTL {
				city := s.maps[key]
				s.cacheUsed[key] = time.Now()
				s.mu.Unlock()
				return trace, city, nil
			}
			s.deleteTraceCacheLocked(key)
		}
		if load := s.inflight[key]; load != nil {
			done := load.done
			shareSnapshot := fingerprint.equal(load.fingerprint)
			s.mu.Unlock()
			<-done

			// Requests that observed the same source version must receive the
			// same trace/city snapshot, even if the active file grows while the
			// shared parse is running. A request that already observed a newer
			// version retries after the older load completes.
			if shareSnapshot {
				return load.trace, load.city, load.err
			}
			continue
		}
		load := &inflightLoad{done: make(chan struct{}), fingerprint: fingerprint}
		s.inflight[key] = load
		s.mu.Unlock()

		// Keep the pre-parse fingerprint. If the active session grows during
		// parsing, the next request will see a mismatch and reload it instead
		// of treating the partial snapshot as current.
		trace, city, err := s.loadTraceAndMap(meta)

		s.mu.Lock()
		if err == nil {
			s.traces[key] = trace
			s.maps[key] = city
			s.cacheFile[key] = fingerprint
			now := time.Now()
			s.cacheAt[key] = now
			s.cacheUsed[key] = now
			s.evictTraceCacheLocked()
		}
		load.trace = trace
		load.city = city
		load.err = err
		delete(s.inflight, key)
		close(load.done)
		s.mu.Unlock()

		return trace, city, err
	}
}

func (s *Server) loadTraceAndMap(meta model.SessionMeta) (*model.Trace, *model.CityMap, error) {
	source := s.adapterForHarness(meta.Harness)
	if source == nil {
		return nil, nil, fmt.Errorf("no adapter for harness %q", meta.Harness)
	}
	trace, parseErr := source.Parse(meta.Path)
	if trace == nil {
		if parseErr != nil {
			return nil, nil, parseErr
		}
		return nil, nil, errors.New("trace unavailable")
	}
	repoRoot := trace.Session.Cwd
	if repoRoot == "" {
		repoRoot = meta.Cwd
	}
	if repoRoot == "" {
		repoRoot = s.cfg.RepoRoot
	}
	if repoRoot == "" {
		repoRoot = filepath.Dir(meta.Path)
	}
	city, err := citymap.Builder{}.Build(repoRoot, trace)
	if err != nil {
		city = emptyCityMap(repoRoot)
	} else {
		assignFileIDs(trace, city)
	}
	// Recompute with the citymap's file count, carrying over the adapter's
	// grade for its error signal — the recount cannot re-derive it.
	trace.Stats = model.ComputeStats(trace, repoFileCount(city), trace.Stats.Observability.Errors)
	redact.Trace(trace)
	return trace, city, nil
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

func (s *Server) deleteTraceCacheLocked(key string) {
	delete(s.traces, key)
	delete(s.maps, key)
	delete(s.cacheAt, key)
	delete(s.cacheUsed, key)
	delete(s.cacheFile, key)
}

func fingerprintFile(path string) (fileFingerprint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileFingerprint{}, err
	}
	return fileFingerprint{size: info.Size(), modTime: info.ModTime()}, nil
}

func (f fileFingerprint) equal(other fileFingerprint) bool {
	return f.size == other.size && f.modTime.Equal(other.modTime)
}

func (s *Server) evictTraceCacheLocked() {
	for len(s.traces) > traceCacheMaxEntries {
		var oldestKey string
		var oldest time.Time
		for key := range s.traces {
			used := s.cacheUsed[key]
			if oldestKey == "" || used.Before(oldest) {
				oldestKey = key
				oldest = used
			}
		}
		if oldestKey == "" {
			return
		}
		s.deleteTraceCacheLocked(oldestKey)
	}
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

func skipSessionFile(source adapter.Source, path string) bool {
	return source.Harness() == "claude-code" && strings.HasPrefix(filepath.Base(path), "agent-")
}

func assignFileIDs(trace *model.Trace, city *model.CityMap) {
	ids := map[string]int{}
	for _, file := range city.Files {
		ids[file.Path] = file.ID
	}
	for ei := range trace.Events {
		for ti := range trace.Events[ei].Targets {
			if id, ok := ids[trace.Events[ei].Targets[ti].Path]; ok {
				v := id
				trace.Events[ei].Targets[ti].FileID = &v
			}
		}
	}
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
