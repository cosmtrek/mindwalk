package crush

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	crushdata "github.com/LarsArtmann/go-crush-data"
	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/judge"
	"github.com/cosmtrek/mindwalk/internal/model"
)

// harnessName is the canonical harness identifier mindwalk attaches to
// every trace that came through this adapter. The frontend matches on
// this string in a few places (filter pills, panel headers, judge
// input shape) so it must stay stable.
const harnessName = "crush"

// ListSessions returns the metadata for every top-level Crush session
// found across all known project databases, newest first. In
// auto-discover mode (Dir == "") it reads the Crush projects registry
// (~/.local/share/crush/projects.json) and queries every project's
// crush.db. When Dir is set explicitly only that one database is used.
//
// Auxiliary (sub-agent) sessions are intentionally hidden from the rail
// because the Agent Lens surfaces them on demand from a root session.
func (a Adapter) ListSessions() ([]model.SessionMeta, error) {
	if a.Dir == "" {
		return a.listAllProjectSessions()
	}

	return a.listSingleDB()
}

// allProjectDBs returns every known Crush database, including the
// global database when it exists. De-duplicated. Used by every multi-DB
// query so the enumeration logic lives in one place.
func allProjectDBs() []projectDB {
	dbs := loadProjectDBs()
	// Also include the global database (it may not appear in the
	// projects registry, e.g. the global config session).
	globalDB := filepath.Join(DefaultDir(), dataDirName, dbName)
	if _, err := os.Stat(globalDB); err == nil {
		if !slices.ContainsFunc(dbs, func(p projectDB) bool { return p.DBPath == globalDB }) {
			dbs = append(dbs, projectDB{DBPath: globalDB})
		}
	}

	return dbs
}

// listAllProjectSessions scans every project database listed in the
// Crush registry and merges the results. Session IDs are globally
// unique UUIDs, so cross-database collisions do not happen in
// practice.
func (a Adapter) listAllProjectSessions() ([]model.SessionMeta, error) {
	projectDBs := allProjectDBs()
	if len(projectDBs) == 0 {
		return nil, nil
	}

	var (
		all         []model.SessionMeta
		oldSchema   []string
		missingCols []string
	)

	for _, pdb := range projectDBs {
		h, err := a.openCached(pdb.DBPath)
		if err != nil || h == nil {
			continue
		}

		if missing := h.missingColumns(); len(missing) > 0 && a.recordOldSchema(pdb.DBPath) {
			oldSchema = append(oldSchema, pdb.DBPath)
			missingCols = unionStrings(missingCols, missing)
		}

		sessions, err := h.inner.Sessions(context.Background(), crushdata.SessionFilter{RootOnly: true})
		if err != nil {
			_ = h.close()

			continue
		}

		cwd := pdb.ProjectPath
		if cwd == "" {
			cwd = a.projectPathForDB(pdb.DBPath)
		}

		for _, cs := range sessions {
			meta := sessionMeta(cs)

			meta.Cwd = cwd
			if a.dbIndex != nil {
				a.dbIndex.Store(meta.ID, pdb.DBPath)
			}

			all = append(all, meta)
		}

		_ = h.close()
	}

	if len(oldSchema) > 0 {
		a.reportOldSchemaSummary(oldSchema, missingCols)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].EndedAt > all[j].EndedAt
	})

	return all, nil
}

func (a Adapter) listSingleDB() ([]model.SessionMeta, error) {
	h, err := a.openReadOnly()
	if err != nil {
		return nil, err
	}

	if h == nil {
		return nil, nil
	}
	defer h.closeDiscard()

	a.warnIfOldSchema(h)
	cwd := a.projectPathForDB(h.path)

	sessions, err := h.inner.Sessions(context.Background(), crushdata.SessionFilter{RootOnly: true})
	if err != nil {
		return nil, fmt.Errorf("list crush sessions: %w", err)
	}

	metas := make([]model.SessionMeta, 0, len(sessions))
	for _, cs := range sessions {
		meta := sessionMeta(cs)
		meta.Cwd = cwd
		metas = append(metas, meta)
	}

	sort.Slice(metas, func(i, j int) bool {
		return metas[i].EndedAt > metas[j].EndedAt
	})

	return metas, nil
}

// Summarize returns the metadata for one session without materialising
// its full message history. The agent-tool (sub-agent) session ID
// format `messageID$$toolCallID` is detected here so the rail can hide
// the corresponding rows from the root listing.
func (a Adapter) Summarize(path string) (model.SessionMeta, error) {
	h, err := a.openDBForPath(path)
	if err != nil {
		return model.SessionMeta{}, err
	}

	if h == nil {
		// No database exists anywhere this adapter can reach, so the
		// session cannot be recognized — report it as foreign rather
		// than as a broken configuration.
		return model.SessionMeta{}, adapter.NotRecognizedErr("Crush", path)
	}
	defer h.closeDiscard()

	id, isAgent, ok := splitSessionID(path)
	if !ok {
		return model.SessionMeta{}, fmt.Errorf("invalid Crush session id in path %q", path)
	}

	cs, err := h.inner.Session(context.Background(), id)
	if err != nil {
		if errors.Is(err, crushdata.ErrSessionNotFound) {
			return model.SessionMeta{}, adapter.NotRecognizedErr("Crush", path)
		}

		return model.SessionMeta{}, err
	}

	meta := sessionMeta(cs)

	meta.Cwd = a.projectPathForDB(h.path)
	if isAgent || meta.Agent != nil {
		meta.Auxiliary = true
	}

	if isAgent {
		parts := strings.SplitN(id, agentIDSeparator, 2)
		if len(parts) == 2 {
			if meta.Agent == nil {
				meta.Agent = &model.AgentSessionMeta{}
			}

			meta.Agent.SourceID = parts[0]
			meta.Agent.LaunchCallID = parts[1]
			// RootSessionID is the parent session id, not the
			// parent message id. sessionMeta already populated
			// it from parent_session_id; only fall back to the
			// source-id split when the row had no parent row.
			if meta.Agent.RootSessionID == "" {
				meta.Agent.RootSessionID = parts[0]
			}
		}
	}
	// Sessions whose cwd matches the mindwalk judge workdir are mindwalk
	// itself recording its judge runs — drop them from every listing.
	if judge.IsWorkDir(meta.Cwd) {
		meta.Auxiliary = true
	}

	adapter.FallbackSessionTitle(&meta, path)

	return meta, nil
}

// Parse converts one Crush session into the shared model.Trace. The
// implementation streams the session's messages once via the
// go-crush-data SDK, folds each message's decoded parts in declaration
// order, and emits one model.Event per resolved tool call. User
// messages with finish.reason=stop also produce a user-message mark so
// the judge layer sees the same task text the rail does.
func (a Adapter) Parse(path string) (*model.Trace, error) {
	h, err := a.openDBForPath(path)
	if err != nil {
		return nil, err
	}

	if h == nil {
		return nil, errDBUnavailable
	}
	defer h.closeDiscard()

	id, _, ok := splitSessionID(path)
	if !ok {
		return nil, fmt.Errorf("invalid Crush session id in path %q", path)
	}

	trace := &model.Trace{
		Version: 1,
		Session: model.TraceSession{
			ID:      id,
			Harness: harnessName,
			Path:    path,
		},
		Events: []model.Event{},
		Marks:  []model.Mark{},
	}

	cs, err := h.inner.Session(context.Background(), id)
	if err != nil {
		if errors.Is(err, crushdata.ErrSessionNotFound) {
			return nil, adapter.NotRecognizedErr("Crush", path)
		}

		return nil, err
	}

	meta := sessionMeta(cs)
	applySessionMeta(trace, meta)
	trace.Session.Cwd = a.projectPathForDB(h.path)

	recognized := meta.ID != ""
	pending := map[string]adapter.ToolCall{}
	pendingOrder := []string{}
	results := map[string]adapter.ToolResult{}
	prevModel := ""
	prevProvider := ""

	// Messages stream one at a time: the cross-message tool_call /
	// tool_result pairing below resolves through the pending and
	// results maps, so no earlier message is ever revisited and only
	// one decoded message is live at once — huge sessions stay flat
	// in memory.
	for msg, err := range h.inner.IterMessages(context.Background(), id) {
		if err != nil {
			return nil, fmt.Errorf("read crush messages: %w", err)
		}
		recognized = true
		ts := timeToRFC3339(msg.CreatedAt)
		applyMessageMeta(trace, msg.Model, msg.Provider, ts)

		// Track model/provider switches mid-session.
		if msg.Model != "" && prevModel != "" && msg.Model != prevModel {
			trace.Marks = append(trace.Marks, model.Mark{
				Seq:  len(pendingOrder),
				Type: "model-switch",
				Note: fmt.Sprintf("%s → %s", prevModel, msg.Model),
			})
		}

		if msg.Provider != "" && prevProvider != "" && msg.Provider != prevProvider {
			trace.Marks = append(trace.Marks, model.Mark{
				Seq:  len(pendingOrder),
				Type: "model-switch",
				Note: fmt.Sprintf("%s → %s", prevProvider, msg.Provider),
			})
		}

		if msg.Model != "" {
			prevModel = msg.Model
		}

		if msg.Provider != "" {
			prevProvider = msg.Provider
		}

		// Malformed parts payloads arrive as nil Parts — the SDK
		// already refused to decode them, so the message folds to
		// an empty result and the trace keeps going.
		parsed := foldParts(msg.Parts, ts)

		// User messages with finish.reason=stop represent the user
		// turn boundary in Crush. The judge needs this so it can see
		// what task the user actually typed without scanning the
		// assistant's replies for the same shape.
		if msg.Role == crushdata.RoleUser && parsed.userFinish && parsed.text != "" {
			trace.Marks = append(trace.Marks, model.Mark{
				Seq:  len(pendingOrder),
				Type: "user-message",
				Note: adapter.UserMessageNote(parsed.text),
			})
		}

		if parsed.subagent && msg.Role == crushdata.RoleAssistant {
			note := parsed.subagentNote
			if note == "" {
				note = parsed.text
			}

			trace.Marks = append(trace.Marks, model.Mark{
				Seq:  len(pendingOrder),
				Type: "subagent",
				Note: note,
			})
		}

		// Reasoning marks surface the agent's inner monologue with
		// duration so the timeline can show thinking vs acting phases.
		if parsed.reasoningText != "" {
			duration := parsed.reasoningSecs
			// Fall back to the message-level wall-clock duration when
			// the reasoning part doesn't carry its own timestamps.
			if duration == 0 && !msg.FinishedAt.IsZero() && msg.FinishedAt.After(msg.CreatedAt) {
				duration = int64(msg.FinishedAt.Sub(msg.CreatedAt) / time.Second)
			}

			note := truncateNote(parsed.reasoningText, 200)
			if duration > 0 {
				note = fmt.Sprintf("thinking %ds: %s", duration, note)
			} else {
				note = "thinking: " + note
			}

			trace.Marks = append(trace.Marks, model.Mark{
				Seq:      len(pendingOrder),
				Type:     "thinking",
				Note:     note,
				Duration: int(duration),
			})
		}

		// Non-normal finish reasons are quality signals — error,
		// content_filter, canceled, max_tokens — and get a mark so
		// the timeline shows why a turn ended unexpectedly.
		switch parsed.finishReason {
		case "error", "content_filter", "canceled", "max_tokens":
			note := parsed.finishReason
			if parsed.finishMessage != "" {
				note = fmt.Sprintf("%s: %s", parsed.finishReason, truncateNote(parsed.finishMessage, 200))
			}

			mk := model.Mark{
				Seq:  len(pendingOrder),
				Type: "finish-reason",
				Note: note,
			}
			if !msg.FinishedAt.IsZero() && msg.FinishedAt.After(msg.CreatedAt) {
				mk.Duration = int(msg.FinishedAt.Sub(msg.CreatedAt) / time.Second)
			}

			trace.Marks = append(trace.Marks, mk)
		}

		for _, call := range parsed.events {
			if _, exists := pending[call.ID]; !exists {
				pendingOrder = append(pendingOrder, call.ID)
			}

			pending[call.ID] = call
			// Match each event to its result by ToolCallID rather than
			// array index. Index alignment assumes every result lives
			// in the same message as its call; when Crush splits a
			// tool_call and tool_result across messages, the orphan
			// results are appended under p.resultOrder in declaration
			// order and partsParser keeps them at the tail of
			// finishResult.results — looking up by ID covers both shapes.
			results[call.ID] = resultFor(parsed.results, call.ID)
		}
		// Fold cross-message results whose originating call lives in
		// an earlier message: the per-call loop above only sees
		// events from THIS message, so any result left in
		// parsed.results without a matching in-message call would
		// otherwise stay unpaired and ComputeStats would mark every
		// such event OutcomeKnown=false.
		for _, crossMessageResult := range parsed.results {
			if crossMessageResult.ToolCallID == "" {
				continue
			}

			if _, seen := pending[crossMessageResult.ToolCallID]; !seen {
				continue
			}

			if existing, ok := results[crossMessageResult.ToolCallID]; ok && existing.OutcomeKnown {
				continue
			}

			results[crossMessageResult.ToolCallID] = crossMessageResult
		}
	}

	for _, id := range pendingOrder {
		call := pending[id]
		result := results[id]
		trace.Events = append(trace.Events, adapter.BuildEvent(trace, call, result))
	}

	sort.SliceStable(trace.Events, func(i, j int) bool {
		return trace.Events[i].Seq < trace.Events[j].Seq
	})

	for i := range trace.Events {
		trace.Events[i].Seq = i
	}

	trace.Session.EventCount = len(trace.Events)
	adapter.FallbackTraceSessionTitle(trace, path)
	// Read the read_files table for exact read observability. The
	// table arrived in a later Crush migration; databases that predate
	// it return an empty set and reads stay estimated.
	readPaths := a.queryReadFiles(h, id)
	if len(readPaths) > 0 {
		for i := range trace.Events {
			for j := range trace.Events[i].Targets {
				t := &trace.Events[i].Targets[j]
				if t.Touch == "read" && readPaths[t.Path] {
					t.Weak = false
				}
			}
		}
	}

	readsGrade := model.ObservabilityEstimated
	if len(readPaths) > 0 {
		readsGrade = model.ObservabilityExact
	}
	// Crush's finish reasons carry no observability flags; the visualizer
	// infers failures from tool_result.is_error the same way it does for
	// Claude Code and Codex.
	trace.Stats = model.ComputeStats(
		trace,
		0,
		model.ObservabilitySignals{Errors: model.ObservabilityExact, Reads: readsGrade},
	)

	if !recognized {
		return nil, adapter.NotRecognizedErr("Crush", path)
	}

	return trace, nil
}

// resultFor finds the ToolResult whose ToolCallID matches toolCallID in
// a parsed message's results slice, returning an empty ToolResult when
// no result was paired. The crush adapter pairs results by ID rather
// than by parallel-slice index because Crush splits tool_call and
// tool_result parts across messages — index alignment would silently
// drop every cross-message outcome.
func resultFor(results []adapter.ToolResult, toolCallID string) adapter.ToolResult {
	for _, r := range results {
		if r.ToolCallID == toolCallID {
			return r
		}
	}

	return adapter.ToolResult{}
}

// queryReadFiles returns the set of file paths the agent actually
// opened, according to the read_files table. Databases that predate
// the table (or fail to answer) yield nil.
func (a Adapter) queryReadFiles(h *dbHandle, sessionID string) map[string]bool {
	paths, err := h.inner.ReadFiles(context.Background(), sessionID)
	if err != nil {
		return nil
	}

	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}

	return set
}

// openReadOnly opens the database in read-only mode, returning (nil,
// nil) when the file is absent so ListSessions can return an empty
// catalog without treating "no Crush installed" as an error. Other
// errors (permission denied, corrupt file, schema mismatch) are
// surfaced with the underlying cause and the resolved path so a
// user can fix the configuration without re-deriving what went
// wrong.
func (a Adapter) openReadOnly() (*dbHandle, error) {
	path := a.dbPath()
	if path == "" {
		return nil, nil
	}

	return a.openCached(path)
}

// openCached opens a database at path, using the adapter's dbCache
// when available. Cached handles are kept open for reuse across
// requests; their close() is a no-op so the connection survives.
func (a Adapter) openCached(path string) (*dbHandle, error) {
	if a.dbCache != nil {
		if v, ok := a.dbCache.Load(path); ok {
			if db, ok := v.(*crushdata.DB); ok && db != nil {
				return &dbHandle{path: path, inner: db, cached: true}, nil
			}
		}
	}

	h, err := openAt(path)
	if err != nil || h == nil {
		return h, err
	}

	if a.dbCache != nil {
		a.dbCache.Store(path, h.inner)
		h.cached = true
	}

	return h, nil
}

// warnIfOldSchema checks whether the database is missing any of the
// well-known columns and prints a deduplicated stderr warning. Returns
// true when the schema is old (columns missing).
func (a Adapter) warnIfOldSchema(h *dbHandle) bool {
	if h == nil || h.path == "" {
		return false
	}

	missing := h.missingColumns()
	if len(missing) == 0 {
		return false
	}

	if !a.recordOldSchema(h.path) {
		return true
	}

	a.reportOldSchemaSummary([]string{h.path}, missing)

	return true
}

// recordOldSchema marks path as having been warned about. It returns
// false when the path was already recorded, so callers can avoid
// duplicate warnings.
func (a Adapter) recordOldSchema(path string) bool {
	if a.warnedOldSchema == nil {
		return true
	}

	if _, already := a.warnedOldSchema.LoadOrStore(path, true); already {
		return false
	}

	return true
}

// reportOldSchemaSummary prints a single stderr warning for one or more
// databases with an old schema. Multiple paths are summarized so a
// host-wide scan does not flood the terminal.
func (a Adapter) reportOldSchemaSummary(paths []string, missing []string) {
	if len(paths) == 0 {
		return
	}

	cols := strings.Join(missing, ", ")
	if len(paths) == 1 {
		fmt.Fprintf(
			os.Stderr,
			"mindwalk: warning: %s has an old schema (missing %s); upgrade Crush to get full trace coverage\n",
			paths[0],
			cols,
		)

		return
	}

	fmt.Fprintf(
		os.Stderr,
		"mindwalk: warning: %d Crush databases have an old schema (missing %s); upgrade Crush to get full trace coverage",
		len(paths),
		cols,
	)

	n := min(3, len(paths))
	if n > 0 {
		fmt.Fprintf(os.Stderr, " (e.g. %s", paths[0])

		for i := 1; i < n; i++ {
			fmt.Fprintf(os.Stderr, ", %s", paths[i])
		}

		if len(paths) > n {
			fmt.Fprint(os.Stderr, ", ...")
		}

		fmt.Fprint(os.Stderr, ")")
	}

	fmt.Fprintln(os.Stderr)
}

// unionStrings returns the union of a and b preserving order.
func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a))
	for _, s := range a {
		seen[s] = true
	}

	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			a = append(a, s)
		}
	}

	return a
}

// enumerateDBPaths returns every database path the adapter should
// consult for a multi-database scan. In explicit-Dir mode it returns
// the single configured database; in auto-discover mode it returns
// every project database plus the global one.
func (a Adapter) enumerateDBPaths() []string {
	if a.Dir != "" {
		if p := a.dbPath(); p != "" {
			return []string{p}
		}

		return nil
	}

	dbs := allProjectDBs()

	paths := make([]string, 0, len(dbs))
	for _, db := range dbs {
		paths = append(paths, db.DBPath)
	}

	return paths
}

// openDBForPath resolves which database holds the session identified by
// path. In explicit-Dir mode it always opens the single configured
// database. In auto-discover mode it consults the sessionDBIndex built
// during ListSessions; on a cache miss it falls back to the resolved
// single database (which covers the mindwalk open/trace CLI paths that
// bypass the server's session scan).
func (a Adapter) openDBForPath(path string) (*dbHandle, error) {
	if a.Dir != "" {
		return a.openReadOnly()
	}

	id, _, ok := splitSessionID(path)
	if ok && a.dbIndex != nil {
		if cached, hit := a.dbIndex.Load(id); hit {
			if dbPath, ok := cached.(string); ok && dbPath != "" {
				return a.openCached(dbPath)
			}
		}
	}

	return a.openReadOnly()
}

// sessionMeta lifts an SDK Session into the shared model and stamps
// the canonical harness key/path. The path doubles as the rail's
// deep-link handle so adapters always return the on-disk location they
// read from.
func sessionMeta(cs crushdata.Session) model.SessionMeta {
	if cs.ParentSessionID != "" {
		// Non-null parent_session_id marks an agent-tool session.
		// The Synthesized path is the agent-tool id (messageID$$toolCallID)
		// so the rail and graph builder can find it back from the
		// parent's agent tool calls.
		agent := &model.AgentSessionMeta{
			SourceID:        cs.ParentSessionID,
			RootSessionID:   cs.ParentSessionID,
			ParentSessionID: cs.ParentSessionID,
		}
		// Crush's own agent-tool session id embeds both the parent
		// message id and the launching tool call id in the row's id
		// column ("messageID$$toolCallID"). Capture them so the graph
		// builder can match launches ↔ children by tool call id.
		if messageID, callID, ok := splitAgentID(cs.ID); ok {
			agent.LaunchCallID = callID
			agent.SourceID = messageID
		}

		return model.SessionMeta{
			Key:              SessionKey(cs.ID),
			ID:               cs.ID,
			Harness:          harnessName,
			Path:             SessionPath(cs.ID),
			Title:            cs.Title,
			StartedAt:        timeToRFC3339(cs.CreatedAt),
			EndedAt:          timeToRFC3339(cs.UpdatedAt),
			EventCount:       cs.MessageCount,
			UserTurns:        0,
			Auxiliary:        true,
			Agent:            agent,
			PromptTokens:     cs.PromptTokens,
			CompletionTokens: cs.CompletionTokens,
			Cost:             cs.CostUSD,
		}
	}

	return model.SessionMeta{
		Key:              SessionKey(cs.ID),
		ID:               cs.ID,
		Harness:          harnessName,
		Path:             SessionPath(cs.ID),
		Title:            cs.Title,
		StartedAt:        timeToRFC3339(cs.CreatedAt),
		EndedAt:          timeToRFC3339(cs.UpdatedAt),
		EventCount:       cs.MessageCount,
		PromptTokens:     cs.PromptTokens,
		CompletionTokens: cs.CompletionTokens,
		Cost:             cs.CostUSD,
	}
}

// applySessionMeta copies the parts of sessionMeta that matter to
// the trace: title, started/ended timestamps, model (best-effort).
// Cwd is not part of Crush's session schema; Parse sets
// trace.Session.Cwd directly from the database path so path
// normalization in BuildEvent can relativise absolute tool-call paths.
func applySessionMeta(trace *model.Trace, meta model.SessionMeta) {
	if meta.Title != "" {
		trace.Session.Title = meta.Title
	}

	if meta.StartedAt != "" {
		trace.Session.StartedAt = meta.StartedAt
	}

	if meta.EndedAt != "" {
		trace.Session.EndedAt = meta.EndedAt
	}

	if meta.Model != "" {
		trace.Session.Model = meta.Model
	}

	if meta.Provider != "" {
		trace.Session.Provider = meta.Provider
	}
}

// applyMessageMeta updates the trace's running cwd/model window from
// one message. Only assistant messages carry a model — every other
// row's model column is empty — so the first non-empty assignment
// wins. The same pattern applies to provider.
func applyMessageMeta(trace *model.Trace, msgModel, msgProvider, ts string) {
	if msgModel != "" && trace.Session.Model == "" {
		trace.Session.Model = msgModel
	}

	if msgProvider != "" && trace.Session.Provider == "" {
		trace.Session.Provider = msgProvider
	}

	if ts != "" {
		if trace.Session.StartedAt == "" {
			trace.Session.StartedAt = ts
		}

		trace.Session.EndedAt = ts
	}
}

// timeToRFC3339 renders an SDK timestamp in the ISO-8601 UTC shape the
// rest of mindwalk expects. The zero time returns "" so an
// empty/uninitialised timestamp doesn't trip downstream parsers.
func timeToRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339Nano)
}

// secondsToRFC3339 renders Crush's second-precision Unix timestamps in
// the ISO-8601 UTC shape the rest of mindwalk expects. Zero or negative
// values return "" so an empty/uninitialised timestamp doesn't trip
// downstream parsers.
//
// IMPORTANT: Crush's database migration comment says "milliseconds", but
// the trigger writes `strftime('%s', 'now')` which is Unix SECONDS. Do not
// "fix" this back to time.UnixMilli — that sends every timestamp to 1970
// (the bug fixed in 3f547fc). The adapter trusts the actual data, not the
// misleading migration comment.
func secondsToRFC3339(s int64) string {
	if s <= 0 {
		return ""
	}

	return time.Unix(s, 0).UTC().Format(time.RFC3339Nano)
}

// truncateNote clips s to maxRunes runes, appending an ellipsis when
// truncation occurs. Used for mark notes that carry user text.
func truncateNote(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}

	return string(runes[:maxRunes]) + "…"
}

// sessionPathScheme is the synthetic-path prefix the server uses to
// deep-link into a Crush session. The actual storage lives inside
// the SQLite database; the path encodes the harness + id so
// adapter.Summarize can recover the row and the Agent Lens can
// route to child sessions via the same id format Crush uses
// internally. Kept as a constant so a future rename is one line.
const sessionPathScheme = "crush://session/"

// SessionPath returns the synthetic handle the server uses to
// deep-link into a Crush session. Callers should treat the returned
// value as opaque and use IsSessionPath / SessionIDFromPath to
// recover the underlying id.
func SessionPath(id string) string {
	return sessionPathScheme + id
}

// IsSessionPath reports whether path is a Crush session handle.
// Useful for adapter-agnostic code in the server that needs to
// distinguish a Crush synthetic path from a real on-disk file.
func IsSessionPath(path string) bool {
	return strings.HasPrefix(path, sessionPathScheme)
}

// SessionIDFromPath extracts the bare session id from a path
// produced by SessionPath. The returned id preserves Crush's
// "messageID$$toolCallID" agent-tool format verbatim — callers
// that need to detect that shape should pass the result through
// splitAgentID.
func SessionIDFromPath(path string) string {
	id, ok := strings.CutPrefix(path, sessionPathScheme)
	if !ok {
		return path
	}

	return id
}

// splitSessionID takes a path produced by SessionPath (or the bare
// session id) and returns the id plus a flag for the agent-tool
// "messageID$$toolCallID" shape.
func splitSessionID(path string) (string, bool, bool) {
	if path == "" {
		return "", false, false
	}

	id := SessionIDFromPath(path)
	if id == "" {
		return "", false, false
	}

	if messageID, callID, ok := splitAgentID(id); ok {
		return messageID + agentIDSeparator + callID, true, true
	}

	return id, false, true
}

// splitAgentID reports whether id looks like Crush's agent-tool
// "messageID$$toolCallID" format and returns the components when it
// does. The dollar-sign-dollar separator is unique to Crush and
// never appears in a UUID or session index, so the match is precise.
func splitAgentID(id string) (string, string, bool) {
	parts := strings.SplitN(id, agentIDSeparator, 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}

	return parts[0], parts[1], true
}

const agentIDSeparator = "$$"

// SessionKey produces the rail/trace key from the canonical session
// id. We re-export adapter.SessionKey with a fixed harness name so
// downstream code reads the same key the server uses in its catalog.
func SessionKey(id string) string {
	return adapter.SessionKey(harnessName, SessionPath(id))
}
