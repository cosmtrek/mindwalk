package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/agents"
	"github.com/cosmtrek/mindwalk/internal/event"
	"github.com/cosmtrek/mindwalk/internal/model"
	"github.com/cosmtrek/mindwalk/internal/redact"
	"github.com/cosmtrek/mindwalk/internal/registry"
)

const quarantineVersion = 1

type ManagerConfig struct {
	Sources      []adapter.Source
	RegistryPath string
	DataRoot     string
}

type Manager struct {
	cfg           ManagerConfig
	log           *event.Log
	store         *StateStore
	qpath         string
	mu            sync.Mutex
	sessions      map[string]SessionStatus
	sources       map[string]SourceStatus
	lastPoll      time.Time
	lastError     string
	quarantine    int64
	rejectedPaths map[string]bool
}

type SourceStatus struct {
	Harness      string `json:"harness"`
	Root         string `json:"root"`
	Available    bool   `json:"available"`
	SessionFiles int    `json:"sessionFiles"`
	LastPoll     string `json:"lastPoll,omitempty"`
	Error        string `json:"error,omitempty"`
}

type SessionStatus struct {
	Key             string `json:"key"`
	ID              string `json:"id"`
	Harness         string `json:"harness"`
	SourceToken     string `json:"sourceToken"`
	RepositoryID    string `json:"repositoryId,omitempty"`
	Association     string `json:"association"`
	AcceptedEvents  int    `json:"acceptedEvents"`
	DurableSequence int64  `json:"durableSequence"`
	State           Status `json:"state"`
	Quarantined     int    `json:"quarantined"`
}

type Health struct {
	Status          string `json:"status"`
	LastPoll        string `json:"lastPoll,omitempty"`
	LastError       string `json:"lastError,omitempty"`
	DurableSequence int64  `json:"durableSequence"`
	QuarantineCount int64  `json:"quarantineCount"`
}

// StreamEvent binds an envelope to its immutable one-based ledger position.
// Envelope.Sequence is source semantics and is deliberately not reused as a
// transport cursor.
type StreamEvent struct {
	Sequence int64          `json:"sequence"`
	Event    event.Envelope `json:"event"`
}

type quarantineEntry struct {
	SchemaVersion int    `json:"schemaVersion"`
	ObservedAt    string `json:"observedAt"`
	SourceToken   string `json:"sourceToken"`
	Reason        string `json:"reason"`
	LineHash      string `json:"lineHash"`
	LineBytes     int    `json:"lineBytes"`
}

func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.DataRoot == "" || cfg.RegistryPath == "" {
		return nil, errors.New("ingest data root and registry path are required")
	}
	store, err := NewStateStore(filepath.Join(cfg.DataRoot, "ingest"))
	if err != nil {
		return nil, err
	}
	log, err := event.OpenLogAt(cfg.DataRoot, filepath.Join("ledger", "events.jsonl"))
	if err != nil {
		return nil, err
	}
	qdir := filepath.Join(cfg.DataRoot, "quarantine")
	if err := os.MkdirAll(qdir, 0o700); err != nil {
		log.Close()
		return nil, err
	}
	m := &Manager{
		cfg:           cfg,
		log:           log,
		store:         store,
		qpath:         filepath.Join(qdir, "records.jsonl"),
		sessions:      map[string]SessionStatus{},
		sources:       map[string]SourceStatus{},
		rejectedPaths: map[string]bool{},
	}
	if count, err := countLines(m.qpath); err == nil {
		m.quarantine = count
	}
	return m, nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.log.Close()
}

func (m *Manager) PollAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	reg, err := registry.Load(m.cfg.RegistryPath)
	if err != nil {
		m.lastError = safeError(err)
		return err
	}
	m.sources = map[string]SourceStatus{}
	var firstErr error
	for _, source := range m.cfg.Sources {
		if err := m.pollSource(source, reg); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.lastPoll = time.Now().UTC()
	if firstErr != nil {
		m.lastError = safeError(firstErr)
		return firstErr
	}
	m.lastError = ""
	return nil
}

func (m *Manager) pollSource(source adapter.Source, reg *registry.Registry) error {
	root, err := canonicalSourceRoot(source.SessionDir())
	status := SourceStatus{Harness: source.Harness(), Root: root, LastPoll: time.Now().UTC().Format(time.RFC3339Nano)}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			m.sources[source.Harness()] = status
			return nil
		}
		status.Error = safeError(err)
		m.sources[source.Harness()] = status
		return err
	}
	status.Available = true
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			return nil
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || !withinRoot(root, canonical) || canonical != path {
			token := sourceToken(source.Harness(), path)
			if !m.rejectedPaths[token] {
				if qerr := m.quarantineLine(token, "source path outside configured canonical root", nil); qerr != nil {
					return qerr
				}
				m.rejectedPaths[token] = true
			}
			return nil
		}
		delete(m.rejectedPaths, sourceToken(source.Harness(), path))
		paths = append(paths, canonical)
		return nil
	})
	if err != nil {
		status.Error = safeError(err)
		m.sources[source.Harness()] = status
		return err
	}
	sort.Strings(paths)
	status.SessionFiles = len(paths)
	m.sources[source.Harness()] = status
	for _, path := range paths {
		if err := m.pollFile(source, path, reg); err != nil && !errors.Is(err, event.ErrDuplicate) {
			return err
		}
	}
	states, err := m.store.List()
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		seen[path] = true
	}
	for token, state := range states {
		if seen[state.Path] || !withinRoot(root, state.Path) || sourceToken(source.Harness(), state.Path) != token {
			continue
		}
		if err := m.pollFile(source, state.Path, reg); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) pollFile(source adapter.Source, path string, reg *registry.Registry) error {
	token := sourceToken(source.Harness(), path)
	state, ok, err := m.store.Load(token)
	if err != nil {
		return err
	}
	if !ok {
		state = State{SchemaVersion: StateVersion, Path: path}
	}
	tailer := Resume(state)
	poll, err := tailer.Poll()
	if err != nil {
		return err
	}
	state = tailer.State()
	if poll.Status == StatusReplaced || poll.Status == StatusTruncated {
		state.ProjectedEvents = 0
		state.LastAcceptedSourceEvent = ""
		state.Blocked = false
	}
	quarantined := 0
	for _, line := range poll.Oversized {
		if err := m.quarantineLine(token, "source line exceeds limit", line); err != nil {
			return err
		}
		quarantined++
		state.Blocked = true
	}
	if poll.SkippedBytes > 0 && len(poll.Oversized) == 0 {
		if err := m.quarantineLine(token, fmt.Sprintf("source line exceeded buffering limit; %d bytes skipped", poll.SkippedBytes), nil); err != nil {
			return err
		}
		quarantined++
		state.Blocked = true
	}
	validLines := 0
	for _, line := range poll.Lines {
		if err := validateSourceLine(line); err != nil {
			if qerr := m.quarantineLine(token, err.Error(), line); qerr != nil {
				return qerr
			}
			quarantined++
			state.Blocked = true
			continue
		}
		validLines++
		sum := sha256.Sum256(line)
		state.LastAcceptedSourceEvent = hex.EncodeToString(sum[:])
	}
	if state.Blocked {
		m.recordSourceQuarantine(token, poll.Status, quarantined)
		return m.store.Save(token, state)
	}
	if poll.Status == StatusMissing {
		m.recordSourceStatus(token, poll.Status)
		return m.store.Save(token, state)
	}
	meta, summaryErr := source.Summarize(path)
	if summaryErr != nil {
		if validLines == 0 {
			return m.store.Save(token, state)
		}
		return summaryErr
	}
	if meta.Auxiliary {
		return m.store.Save(token, state)
	}
	repoID, association := associateRepository(meta.Cwd, reg)
	session := SessionStatus{Key: meta.Key, ID: meta.ID, Harness: source.Harness(), SourceToken: token, RepositoryID: repoID, Association: association, State: poll.Status, Quarantined: quarantined}
	if association != "EXACT" {
		m.sessions[meta.Key] = session
		return m.store.Save(token, state)
	}
	trace, parseErr := source.Parse(path)
	if trace == nil {
		if parseErr != nil {
			return parseErr
		}
		return errors.New("adapter returned no trace")
	}
	redact.Trace(trace)
	if err := m.appendSessionStart(trace, meta.Key, repoID, source.Harness()); err != nil && !errors.Is(err, event.ErrDuplicate) {
		return err
	}
	projected := 0
	for i := range trace.Events {
		if !trace.Events[i].Complete {
			continue
		}
		projected++
		envelope, err := normalizeTraceEvent(trace, trace.Events[i], meta.Key, repoID, source.Harness())
		if err != nil {
			return err
		}
		if err := m.log.Append(envelope); err != nil && !errors.Is(err, event.ErrDuplicate) {
			return err
		}
	}
	for _, mark := range trace.Marks {
		envelope, err := normalizeMark(trace, mark, meta.Key, repoID, source.Harness())
		if err != nil {
			return err
		}
		if err := m.log.Append(envelope); err != nil && !errors.Is(err, event.ErrDuplicate) {
			return err
		}
	}
	state.ProjectedEvents = projected
	state.DurableSequence = int64(m.log.Len())
	session.AcceptedEvents = projected
	session.DurableSequence = state.DurableSequence
	m.sessions[meta.Key] = session
	return m.store.Save(token, state)
}

func (m *Manager) recordSourceQuarantine(token string, status Status, count int) {
	for key, session := range m.sessions {
		if session.SourceToken == token {
			session.State = status
			session.Quarantined += count
			m.sessions[key] = session
		}
	}
}

func (m *Manager) recordSourceStatus(token string, status Status) {
	for key, session := range m.sessions {
		if session.SourceToken == token {
			session.State = status
			m.sessions[key] = session
		}
	}
}

func (m *Manager) appendSessionStart(trace *model.Trace, sessionKey, repoID, harness string) error {
	timestamp := canonicalTimestamp(trace.Session.StartedAt)
	sessionID := sessionKey
	envelope, err := event.Finalize(event.Envelope{
		SchemaVersion: event.SchemaVersion,
		EventType:     event.TypeSessionStarted,
		OccurredAt:    timestamp, ObservedAt: timestamp, Sequence: 0,
		RepoID: &repoID, SessionID: &sessionID,
		Attrs:      map[string]string{"harness": harness, "sourceSessionId": trace.Session.ID},
		Provenance: derivedProvenance(harness, "session"),
	})
	if err != nil {
		return err
	}
	return m.log.Append(envelope)
}

func normalizeTraceEvent(trace *model.Trace, item model.Event, sessionKey, repoID, harness string) (event.Envelope, error) {
	timestamp := canonicalTimestamp(item.Timestamp)
	sessionID := sessionKey
	attrs := map[string]string{
		"tool":            item.Tool,
		"action":          item.Action,
		"error":           strconv.FormatBool(item.IsError),
		"resultBytes":     strconv.Itoa(item.ResultBytes),
		"sourceSessionId": trace.Session.ID,
	}
	if len(item.Targets) > 0 && item.Targets[0].Path != "" {
		attrs["path"] = item.Targets[0].Path
		attrs["touch"] = item.Targets[0].Touch
	}
	typ := event.TypeToolCompleted
	switch item.Action {
	case "search":
		typ = event.TypeFileSearched
	case "read":
		typ = event.TypeFileRead
	case "edit":
		typ = event.TypeFileEdited
	case "exec":
		typ = event.TypeCommandCompleted
	case "verify":
		typ = event.TypeVerifyCompleted
	}
	if item.IsError && item.Action != "verify" {
		typ = event.TypeToolFailed
	}
	semantic := item
	semantic.Seq = 0
	semanticBytes, _ := json.Marshal(semantic)
	semanticHash := sha256.Sum256(semanticBytes)
	sourceID := "trace_" + hex.EncodeToString(semanticHash[:16])
	return event.Finalize(event.Envelope{
		SchemaVersion: event.SchemaVersion, EventType: typ,
		OccurredAt: timestamp, ObservedAt: timestamp, Sequence: 0,
		RepoID: &repoID, SessionID: &sessionID, Attrs: attrs,
		Provenance: derivedProvenance(harness, sourceID),
	})
}

func normalizeMark(trace *model.Trace, mark model.Mark, sessionKey, repoID, harness string) (event.Envelope, error) {
	typ := event.TypeSystemHeartbeat
	switch mark.Type {
	case "subagent":
		typ = event.TypeAgentSpawned
	case "user-message":
		typ = event.TypeUserMessage
	case "compaction":
		typ = event.TypeContextCompacted
	}
	sessionID := sessionKey
	timestamp := canonicalTimestamp(trace.Session.StartedAt)
	attrs := map[string]string{"markType": mark.Type, "sourceSessionId": trace.Session.ID}
	if mark.Type == "subagent" && mark.Note != "" {
		attrs["agentKind"] = mark.Note
	}
	return event.Finalize(event.Envelope{
		SchemaVersion: event.SchemaVersion, EventType: typ,
		OccurredAt: timestamp, ObservedAt: timestamp, Sequence: int64(mark.Seq),
		RepoID: &repoID, SessionID: &sessionID,
		Attrs:      attrs,
		Provenance: derivedProvenance(harness, fmt.Sprintf("mark:%s:%d", mark.Type, mark.Seq)),
	})
}

func derivedProvenance(harness, sourceID string) event.Provenance {
	return event.Provenance{
		SourceType: harness, SourceName: harness, SourceEventID: &sourceID,
		Quality: event.QualityDerived, Confidence: floatPointer(1),
		Explanation: "mapped from the existing source adapter normalized trace",
	}
}

func floatPointer(v float64) *float64 { return &v }

func validateSourceLine(line []byte) error {
	if len(line) == 0 || len(line) > DefaultMaxLineBytes {
		return errors.New("invalid source line size")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(line, &object); err != nil {
		return errors.New("malformed source JSON")
	}
	if len(object) == 0 {
		return errors.New("source JSON object is empty")
	}
	return nil
}

func canonicalSourceRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("source root is not configured")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("source root unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("source root is not a directory")
	}
	return canonical, nil
}

func withinRoot(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func associateRepository(cwd string, reg *registry.Registry) (string, string) {
	var matches []string
	for _, repo := range reg.List() {
		if !repo.Enabled {
			continue
		}
		status, err := reg.StatusOf(repo.ID)
		if err == nil && !status.Missing && !status.InvalidPath && registry.Within(repo.Path, cwd) {
			matches = append(matches, repo.ID)
		}
	}
	if len(matches) == 1 {
		return matches[0], "EXACT"
	}
	return "", "UNKNOWN"
}

func sourceToken(harness, path string) string {
	sum := sha256.Sum256([]byte(harness + "\x00" + filepath.Clean(path)))
	return "src_" + hex.EncodeToString(sum[:12])
}

func canonicalTimestamp(value string) string {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC().Format(time.RFC3339Nano)
	}
	return "1970-01-01T00:00:00Z"
}

func (m *Manager) quarantineLine(token, reason string, line []byte) error {
	sum := sha256.Sum256(line)
	record := quarantineEntry{
		SchemaVersion: quarantineVersion,
		ObservedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		SourceToken:   token, Reason: reason,
		LineHash: hex.EncodeToString(sum[:]), LineBytes: len(line),
	}
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(m.qpath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	m.quarantine++
	return nil
}

func (m *Manager) Sources() []SourceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SourceStatus, 0, len(m.sources))
	for _, status := range m.sources {
		out = append(out, status)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Harness < out[j].Harness })
	return out
}

func (m *Manager) Sessions() []SessionStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SessionStatus, 0, len(m.sessions))
	for _, status := range m.sessions {
		out = append(out, status)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (m *Manager) Events(sessionID string) ([]event.Envelope, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all, err := event.ReadAll(filepath.Join(m.cfg.DataRoot, "ledger", "events.jsonl"))
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		return all, nil
	}
	var out []event.Envelope
	for _, envelope := range all {
		if envelope.SessionID != nil && *envelope.SessionID == sessionID {
			out = append(out, envelope)
		}
	}
	return out, nil
}

func (m *Manager) EventsAfter(sequence int64, sessionID string, limit int) ([]StreamEvent, int64, bool, error) {
	all, err := m.Events("")
	if err != nil {
		return nil, 0, false, err
	}
	latest := int64(len(all))
	if sequence < 0 {
		sequence = 0
	}
	// Global ledger indexes are the durable SSE sequence. Filter only after
	// assigning that index so cursors never change with session filtering.
	var out []StreamEvent
	for i, envelope := range all {
		seq := int64(i + 1)
		if seq <= sequence {
			continue
		}
		if sessionID != "" && (envelope.SessionID == nil || *envelope.SessionID != sessionID) {
			continue
		}
		out = append(out, StreamEvent{Sequence: seq, Event: envelope})
		if limit > 0 && len(out) >= limit {
			return out, latest, true, nil
		}
	}
	return out, latest, false, nil
}

func (m *Manager) Projection(sessionID string) (ObservableProjection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	projector := NewObservableProjector(sessionID)
	if err := event.Replay(filepath.Join(m.cfg.DataRoot, "ledger", "events.jsonl"), projector); err != nil {
		return ObservableProjection{}, err
	}
	return projector.Snapshot(), nil
}

func (m *Manager) AgentProcesses(sessionID string) ([]agents.Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	projector := agents.NewProjector(sessionID)
	if err := event.Replay(filepath.Join(m.cfg.DataRoot, "ledger", "events.jsonl"), projector); err != nil {
		return nil, err
	}
	return projector.Snapshot(), nil
}

func (m *Manager) Health() Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := "idle"
	if !m.lastPoll.IsZero() {
		status = "live"
	}
	if m.lastError != "" {
		status = "failed"
	}
	last := ""
	if !m.lastPoll.IsZero() {
		last = m.lastPoll.Format(time.RFC3339Nano)
	}
	return Health{Status: status, LastPoll: last, LastError: m.lastError, DurableSequence: int64(m.log.Len()), QuarantineCount: m.quarantine}
}

func countLines(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return int64(strings.Count(string(b), "\n")), nil
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	text, _ := redact.String(err.Error())
	return text
}
