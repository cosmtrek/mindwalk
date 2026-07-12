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
	"strings"
	"sync"
	"time"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/adapter/claudecode"
	"github.com/cosmtrek/mindwalk/internal/adapter/codex"
	"github.com/cosmtrek/mindwalk/internal/citymap"
	"github.com/cosmtrek/mindwalk/internal/model"
)

//go:embed static
var embeddedStatic embed.FS

type Config struct {
	Port        int
	ClaudeDir   string
	CodexDir    string
	OpenSession string
	Dev         bool
	RepoRoot    string
	MapOnly     bool
}

type Server struct {
	cfg       Config
	adapters  []adapter.Source
	mu        sync.Mutex
	scanMu    sync.Mutex
	sessions  []model.SessionMeta
	sessionAt time.Time
	freshGen  uint64
	traces    map[string]*model.Trace
	maps      map[string]*model.CityMap
	cacheAt   map[string]time.Time
	cacheUsed map[string]time.Time
	cacheFile map[string]fileFingerprint
	inflight  map[string]*inflightLoad
	summaries map[string]summaryCacheEntry
	repoMaps  map[string]repoMapEntry
	repoMapMu sync.Mutex
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
	return &Server{
		cfg:       cfg,
		adapters:  []adapter.Source{claudecode.Adapter{Dir: cfg.ClaudeDir}, codex.Adapter{Dir: cfg.CodexDir}},
		traces:    map[string]*model.Trace{},
		maps:      map[string]*model.CityMap{},
		cacheAt:   map[string]time.Time{},
		cacheUsed: map[string]time.Time{},
		cacheFile: map[string]fileFingerprint{},
		inflight:  map[string]*inflightLoad{},
		summaries: map[string]summaryCacheEntry{},
		repoMaps:  map[string]repoMapEntry{},
	}
}

func (s *Server) Start(openBrowser bool) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/sessions/", s.handleSessionResource)
	mux.HandleFunc("/api/repomap", s.handleRepoMap)
	mux.HandleFunc("/", s.handleStatic)

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
	return http.Serve(ln, mux)
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
	default:
		http.NotFound(w, r)
	}
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
		if meta != nil && !meta.Auxiliary {
			sessions = append(sessions, *meta)
		}
	}
	s.pruneSummaryCache(seen)
	return sessions, nil
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
