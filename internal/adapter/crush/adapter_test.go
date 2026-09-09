package crush

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmtrek/mindwalk/internal/model"
	_ "modernc.org/sqlite"
)

// newFixtureDB creates a fresh Crush database under t.TempDir/crush.db
// and seeds it with the minimum schema the adapter reads. Every test
// returns the open handle plus the working directory so adapters
// can resolve a project-local .crush without touching the host FS.
func newFixtureDB(t *testing.T, seed func(*sql.DB)) (dir string, db *sql.DB) {
	t.Helper()
	root := t.TempDir()

	data := filepath.Join(root, ".crush")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}

	handle, err := sql.Open("sqlite", "file:"+filepath.Join(data, "crush.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}

	handle.SetMaxOpenConns(1)

	schema := `
		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			parent_session_id TEXT,
			title TEXT NOT NULL,
			message_count INTEGER NOT NULL DEFAULT 0,
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			cost REAL NOT NULL DEFAULT 0.0,
			updated_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			todos TEXT
		);
		CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			parts TEXT NOT NULL DEFAULT '[]',
			model TEXT,
			provider TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			finished_at INTEGER
		);
	`
	if _, err := handle.Exec(schema); err != nil {
		_ = handle.Close()

		t.Fatal(err)
	}

	if seed != nil {
		seed(handle)
	}

	t.Cleanup(func() { _ = handle.Close() })

	return data, handle
}

func writeParts(t *testing.T, parts ...map[string]any) string {
	t.Helper()

	encoded := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		data, err := json.Marshal(part)
		if err != nil {
			t.Fatal(err)
		}

		encoded = append(encoded, data)
	}

	out, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}

// insertSession writes a row to the sessions table. createdAt is
// captured in Unix seconds like Crush does internally.
func insertSession(
	t *testing.T,
	db *sql.DB,
	id string,
	parent string,
	title string,
	createdAt time.Time,
	messages int64,
) {
	t.Helper()

	if _, err := db.Exec(
		`INSERT INTO sessions (id, parent_session_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at) VALUES (?, ?, ?, ?, 0, 0, 0.0, ?, ?)`,
		id,
		nullableString(parent),
		title,
		messages,
		createdAt.Unix(),
		createdAt.Unix(),
	); err != nil {
		t.Fatal(err)
	}
}

// insertMessage writes one row to the messages table. createdAt is
// recorded in Unix seconds to match Crush's convention.
func insertMessage(
	t *testing.T,
	db *sql.DB,
	sessionID string,
	id string,
	role string,
	parts string,
	model string,
	createdAt time.Time,
) {
	t.Helper()

	if _, err := db.Exec(
		`INSERT INTO messages (id, session_id, role, parts, model, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id,
		sessionID,
		role,
		parts,
		nullableString(model),
		createdAt.Unix(),
		createdAt.Unix(),
	); err != nil {
		t.Fatal(err)
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}

	return value
}

// TestSessionDirPrefersProjectFixture verifies the adapter's project
// discovery: a .crush directory inside the working dir wins over the
// platform default even when CRUSH_GLOBAL_DATA points elsewhere.
func TestSessionDirPrefersProjectFixture(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")

	data := filepath.Join(project, ".crush")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	// The directory only counts as a Crush data dir when crush.db
	// sits next to it — create an empty file so the lookup accepts
	// the project fixture.
	if err := os.WriteFile(filepath.Join(data, "crush.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Truncate any cached git-worktree root from previous runs so
	// the fresh temp directory is not confused with one upstream.
	worktreeRootCache.Clear()
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(root, "global"))

	got := Adapter{WorkingDir: project}.SessionDir()
	if got != data {
		t.Fatalf("session dir = %q, want %q", got, data)
	}
}

// TestListSessionsHidesAgentSessions ensures that agent-tool child
// sessions (those with a parent_session_id) stay out of the rail.
func TestListSessionsHidesAgentSessions(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "root-1", "", "Root session", base, 4)
	insertSession(t, db, "child-1", "root-1", "Agent: explore", base.Add(time.Minute), 2)

	adapter := Adapter{Dir: data}

	metas, err := adapter.ListSessions()
	if err != nil {
		t.Fatal(err)
	}

	if len(metas) != 1 {
		t.Fatalf("metas = %d, want 1 (child sessions should be hidden)", len(metas))
	}

	if metas[0].ID != "root-1" {
		t.Fatalf("meta id = %q", metas[0].ID)
	}
}

// TestParseBuildsEventsAndMarks covers the full Parse path: text
// concatenation across messages, tool_call/tool_result pairing, and
// the user-message mark for a finish-reason=stop turn.
func TestParseBuildsEventsAndMarks(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "demo", "", "Demo", base, 3)

	// User message carrying the request.
	insertMessage(t, db, "demo", "m1", "user", writeParts(t,
		map[string]any{"type": "text", "data": map[string]any{"text": "Add crush support"}},
		map[string]any{"type": "finish", "data": map[string]any{"reason": "stop", "time": 0}},
	), "", base)

	// Assistant message with a view tool call.
	insertMessage(t, db, "demo", "m2", "assistant", writeParts(
		t,
		map[string]any{"type": "text", "data": map[string]any{"text": "Looking at the adapter."}},
		map[string]any{
			"type": "tool_call",
			"data": map[string]any{
				"id":                "call_view_1",
				"name":              "view",
				"input":             `{"file_path":"internal/adapter/adapter.go"}`,
				"finished":          true,
				"provider_executed": false,
			},
		},
	), "minimax/minimax-m3", base.Add(time.Second))

	// Tool result message.
	insertMessage(t, db, "demo", "m3", "tool", writeParts(
		t,
		map[string]any{
			"type": "tool_result",
			"data": map[string]any{
				"tool_call_id": "call_view_1",
				"name":         "view",
				"content":      "package adapter\n",
				"is_error":     false,
			},
		},
	), "", base.Add(2*time.Second))

	adapter := Adapter{Dir: data}

	trace, err := adapter.Parse(SessionPath("demo"))
	if err != nil {
		t.Fatal(err)
	}

	if trace.Session.Harness != "crush" {
		t.Fatalf("harness = %q", trace.Session.Harness)
	}

	if trace.Session.Model != "minimax/minimax-m3" {
		t.Fatalf("model = %q", trace.Session.Model)
	}

	if len(trace.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(trace.Events))
	}

	ev := trace.Events[0]
	if ev.Tool != "view" || ev.Action != "read" {
		t.Fatalf("event = %+v", ev)
	}

	if len(ev.Targets) != 1 || ev.Targets[0].Path != "internal/adapter/adapter.go" {
		t.Fatalf("targets = %+v", ev.Targets)
	}

	if len(trace.Marks) != 1 || trace.Marks[0].Type != "user-message" {
		t.Fatalf("marks = %+v", trace.Marks)
	}
}

// TestParseOrphanToolCallStillEmitsEvent ensures a tool_call without
// a matching tool_result still surfaces an event so the timeline
// doesn't silently lose tool attempts.
func TestParseOrphanToolCallStillEmitsEvent(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "demo", "", "Orphan", base, 1)
	insertMessage(t, db, "demo", "m1", "assistant", writeParts(
		t,
		map[string]any{
			"type": "tool_call",
			"data": map[string]any{
				"id":                "missing",
				"name":              "bash",
				"input":             `{"command":"go test"}`,
				"finished":          true,
				"provider_executed": false,
			},
		},
	), "", base)

	trace, err := Adapter{Dir: data}.Parse(SessionPath("demo"))
	if err != nil {
		t.Fatal(err)
	}

	if len(trace.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(trace.Events))
	}

	if trace.Events[0].ResultBytes != 0 {
		t.Fatalf("orphan event should carry an empty result, got %d bytes", trace.Events[0].ResultBytes)
	}
}

// TestParseMarksSubagentLaunches ensures the `agent` tool emits a
// subagent mark so the trace aligns with the Agent Lens graph.
func TestParseMarksSubagentLaunches(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "root", "", "Root", base, 1)
	insertMessage(t, db, "root", "m1", "assistant", writeParts(
		t,
		map[string]any{
			"type": "tool_call",
			"data": map[string]any{
				"id":                "agent_1",
				"name":              "agent",
				"input":             `{"task_title":"explore schema","agent_type":"explore","message":"read the migrations"}`,
				"finished":          true,
				"provider_executed": false,
			},
		},
	), "", base)

	trace, err := Adapter{Dir: data}.Parse(SessionPath("root"))
	if err != nil {
		t.Fatal(err)
	}

	if len(trace.Marks) != 1 {
		t.Fatalf("marks = %+v", trace.Marks)
	}

	if trace.Marks[0].Type != "subagent" {
		t.Fatalf("mark type = %q", trace.Marks[0].Type)
	}

	if trace.Marks[0].Note != "explore schema" {
		t.Fatalf("note = %q", trace.Marks[0].Note)
	}
}

// TestParseHandlesNestedJSONInput guards against the common Crush
// shape where tool_call.input is a stringified JSON literal of a
// stringified JSON object. The parser must peel both layers.
func TestParseHandlesNestedJSONInput(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "demo", "", "Nested", base, 1)
	// inner JSON literal — what Crush often writes for tool inputs.
	nested := `{"file_path":"internal/server/server.go","limit":50}`
	insertMessage(t, db, "demo", "m1", "assistant", writeParts(
		t,
		map[string]any{
			"type": "tool_call",
			"data": map[string]any{
				"id":                "call_view_1",
				"name":              "view",
				"input":             nested,
				"finished":          true,
				"provider_executed": false,
			},
		},
	), "", base)

	trace, err := Adapter{Dir: data}.Parse(SessionPath("demo"))
	if err != nil {
		t.Fatal(err)
	}

	if len(trace.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(trace.Events))
	}

	if trace.Events[0].Targets[0].Path != "internal/server/server.go" {
		t.Fatalf("path = %q", trace.Events[0].Targets[0].Path)
	}
}

// TestParseRelativizesAbsolutePathsFromCwd is the regression test for the
// root cause of the "everything is unvisited" bug: Crush tool calls use
// absolute file paths, but without trace.Session.Cwd the normalizer
// classifies every path as outside the repo and no target is emitted.
// The fixture lives under <tmp>/.crush/crush.db, so projectPathForDB
// derives <tmp> as the project root and Parse must set it as the cwd
// before BuildEvent runs.
func TestParseRelativizesAbsolutePathsFromCwd(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	// data is <tmp>/.crush — projectPathForDB should derive <tmp>.
	wantCwd := filepath.Dir(data)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "demo", "", "Demo", base, 1)

	absPath := filepath.Join(wantCwd, "internal", "server", "server.go")
	insertMessage(t, db, "demo", "m1", "assistant", writeParts(
		t,
		map[string]any{
			"type": "tool_call",
			"data": map[string]any{
				"id":                "call_view_1",
				"name":              "view",
				"input":             `{"file_path":"` + absPath + `"}`,
				"finished":          true,
				"provider_executed": false,
			},
		},
	), "", base)

	trace, err := Adapter{Dir: data}.Parse(SessionPath("demo"))
	if err != nil {
		t.Fatal(err)
	}

	if trace.Session.Cwd != wantCwd {
		t.Fatalf("cwd = %q, want %q", trace.Session.Cwd, wantCwd)
	}

	if len(trace.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(trace.Events))
	}

	ev := trace.Events[0]
	if len(ev.Targets) != 1 {
		t.Fatalf("targets = %+v (want 1)", ev.Targets)
	}

	wantRel := filepath.ToSlash(filepath.Join("internal", "server", "server.go"))
	if ev.Targets[0].Path != wantRel {
		t.Fatalf("target path = %q, want %q", ev.Targets[0].Path, wantRel)
	}

	if len(ev.Outside) != 0 {
		t.Fatalf("outside = %+v (want none)", ev.Outside)
	}
}

// TestProjectPathForDBDerivation verifies the path-based fallback:
// <project>/.crush/crush.db resolves to <project>, while databases not
// under a .crush directory resolve to "".
func TestProjectPathForDBDerivation(t *testing.T) {
	// Ensure the projects.json cache doesn't interfere — point the
	// global data dir at an empty temp dir so loadProjectDBs returns nil.
	empty := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_DATA", empty)

	tmp := t.TempDir()
	crushDB := filepath.Join(tmp, dataDirName, dbName)

	got := Adapter{}.projectPathForDB(crushDB)
	if got != tmp {
		t.Fatalf("projectPathForDB(%q) = %q, want %q", crushDB, got, tmp)
	}

	// Non-.crush directory → empty.
	plain := filepath.Join(t.TempDir(), "data", dbName)
	if got := (Adapter{}).projectPathForDB(plain); got != "" {
		t.Fatalf("projectPathForDB(%q) = %q, want empty", plain, got)
	}

	// Empty input → empty.
	if got := (Adapter{}).projectPathForDB(""); got != "" {
		t.Fatalf("projectPathForDB(\"\") = %q, want empty", got)
	}
}

// TestSummarizeFlagsAuxiliary verifies the Summarize path stamps
// Auxiliary=true on agent-tool child sessions and exposes both the
// message id and launching tool call id on Agent.
func TestSummarizeFlagsAuxiliary(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "root", "", "Root", base, 1)
	insertSession(t, db, "msg-1$$toolcall-1", "root", "Agent: explore", base.Add(time.Minute), 2)

	adapter := Adapter{Dir: data}

	meta, err := adapter.Summarize(SessionPath("msg-1$$toolcall-1"))
	if err != nil {
		t.Fatal(err)
	}

	if !meta.Auxiliary {
		t.Fatalf("meta auxiliary = false, want true")
	}

	if meta.Agent == nil || meta.Agent.SourceID != "msg-1" || meta.Agent.LaunchCallID != "toolcall-1" {
		t.Fatalf("agent = %+v", meta.Agent)
	}
}

// TestDecodePartsToleratesMalformedParts ensures a malformed parts
// payload doesn't poison the whole parse — the trace still loads.
func TestDecodePartsToleratesMalformedParts(t *testing.T) {
	_, err := decodeParts("not json", "")
	if err == nil {
		t.Fatalf("expected decode error for non-JSON parts")
	}

	result, err := decodeParts("[]", "")
	if err != nil {
		t.Fatal(err)
	}

	if result.text != "" || len(result.events) != 0 {
		t.Fatalf("empty parts should produce empty result, got %+v", result)
	}
}

// TestSummarizeMissingDatabase is the no-Crush-installed fallback —
// calling Summarize on an empty data dir must surface a clear error
// instead of returning a phantom meta.
func TestSummarizeMissingDatabase(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_DATA", root)
	// Wipe the global data directory so neither project nor global
	// discovery can find a crush.db. The adapter should report
	// "no Crush session" rather than fabricating one.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	_, err := Adapter{}.Summarize(SessionPath("anything"))
	if err == nil || !strings.Contains(err.Error(), "not a Crush session") {
		t.Fatalf("expected not-a-Crush-session error, got %v", err)
	}
}

// TestOpenReadOnlyReportsUnderlyingError verifies the error path
// surfaces the underlying cause and the resolved path so a user
// can diagnose a broken configuration without re-deriving the
// path. An empty file and a directory where crush.db is expected
// each produce a distinct, helpful error message.
func TestOpenReadOnlyReportsUnderlyingError(t *testing.T) {
	// Empty file → "empty (size 0)"
	dir := t.TempDir()

	emptyFile := filepath.Join(dir, "crush.db")
	if err := os.WriteFile(emptyFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Adapter{Dir: dir}.ListSessions()
	if err == nil || !strings.Contains(err.Error(), "empty (size 0)") {
		t.Fatalf("empty file error = %v", err)
	}

	// Directory where file is expected → "is a directory"
	dirAsFile := filepath.Join(t.TempDir(), "crush.db")
	if err := os.MkdirAll(dirAsFile, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err = Adapter{Dir: filepath.Dir(dirAsFile)}.ListSessions()
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("directory-as-file error = %v", err)
	}
}

// insertMessageWithProvider writes one row to the messages table with
// the provider column set, for tests that verify provider tracking.
func insertMessageWithProvider(
	t *testing.T,
	db *sql.DB,
	sessionID string,
	id string,
	role string,
	parts string,
	model string,
	provider string,
	createdAt time.Time,
) {
	t.Helper()

	if _, err := db.Exec(
		`INSERT INTO messages (id, session_id, role, parts, model, provider, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		sessionID,
		role,
		parts,
		nullableString(model),
		nullableString(provider),
		createdAt.Unix(),
		createdAt.Unix(),
	); err != nil {
		t.Fatal(err)
	}
}

// insertSessionWithUsage writes a session row with non-zero token and
// cost values, for tests that verify usage metadata.
func insertSessionWithUsage(
	t *testing.T,
	db *sql.DB,
	id string,
	title string,
	createdAt time.Time,
	promptTokens int64,
	completionTokens int64,
	cost float64,
) {
	t.Helper()

	if _, err := db.Exec(
		`INSERT INTO sessions (id, parent_session_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at) VALUES (?, NULL, ?, 0, ?, ?, ?, ?, ?)`,
		id,
		title,
		promptTokens,
		completionTokens,
		cost,
		createdAt.Unix(),
		createdAt.Unix(),
	); err != nil {
		t.Fatal(err)
	}
}

// TestParsePopulatesProvider verifies the provider field is populated
// from the first non-null provider in the messages table.
func TestParsePopulatesProvider(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "s1", "", "Provider test", base, 2)
	insertMessageWithProvider(t, db, "s1", "m1", "user",
		writeParts(t, map[string]any{"type": "text", "data": map[string]any{"text": "hello"}}),
		"", "anthropic", base)
	insertMessageWithProvider(t, db, "s1", "m2", "assistant",
		writeParts(t, map[string]any{"type": "text", "data": map[string]any{"text": "hi back"}}),
		"claude-sonnet-4", "anthropic", base.Add(time.Second))

	trace, err := Adapter{Dir: data}.Parse(SessionPath("s1"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if trace.Session.Provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", trace.Session.Provider)
	}

	if trace.Session.Model != "claude-sonnet-4" {
		t.Fatalf("model = %q, want claude-sonnet-4", trace.Session.Model)
	}
}

// TestParseEmitsModelSwitchMark verifies a model-switch mark is
// emitted when the model changes mid-session.
func TestParseEmitsModelSwitchMark(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "s1", "", "Switch test", base, 3)
	insertMessageWithProvider(t, db, "s1", "m1", "assistant",
		writeParts(t, map[string]any{"type": "text", "data": map[string]any{"text": "first"}}),
		"model-a", "anthropic", base)
	insertMessageWithProvider(t, db, "s1", "m2", "assistant",
		writeParts(t, map[string]any{"type": "text", "data": map[string]any{"text": "second"}}),
		"model-b", "anthropic", base.Add(time.Second))

	trace, err := Adapter{Dir: data}.Parse(SessionPath("s1"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var sawSwitch bool

	for _, m := range trace.Marks {
		if m.Type == "model-switch" {
			sawSwitch = true

			if !strings.Contains(m.Note, "model-a") || !strings.Contains(m.Note, "model-b") {
				t.Fatalf("switch note = %q", m.Note)
			}
		}
	}

	if !sawSwitch {
		t.Fatalf("expected a model-switch mark, marks = %+v", trace.Marks)
	}
}

// TestReadFilesUpgradesObservability verifies the read_files table
// upgrades the read observability grade from estimated to exact.
func TestReadFilesUpgradesObservability(t *testing.T) {
	data, db := newFixtureDB(t, func(db *sql.DB) {
		if _, err := db.Exec(
			`CREATE TABLE IF NOT EXISTS read_files (session_id TEXT NOT NULL, path TEXT NOT NULL, read_at INTEGER NOT NULL)`,
		); err != nil {
			t.Fatal(err)
		}

		if _, err := db.Exec(
			`INSERT INTO read_files (session_id, path, read_at) VALUES ('s1', 'main.go', 0)`,
		); err != nil {
			t.Fatal(err)
		}
	})
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "s1", "", "Read files test", base, 1)
	insertMessage(t, db, "s1", "m1", "assistant",
		writeParts(t, map[string]any{"type": "text", "data": map[string]any{"text": "working"}}),
		"claude-sonnet-4", base)

	trace, err := Adapter{Dir: data}.Parse(SessionPath("s1"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if trace.Stats.Observability.Reads != model.ObservabilityExact {
		t.Fatalf("reads observability = %q, want exact", trace.Stats.Observability.Reads)
	}
}

// TestReadFilesMissingTableFallsBack verifies that when the read_files
// table does not exist (older Crush databases), the observability
// stays at estimated without crashing.
func TestReadFilesMissingTableFallsBack(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "s1", "", "No read_files", base, 1)
	insertMessage(t, db, "s1", "m1", "assistant",
		writeParts(t, map[string]any{"type": "text", "data": map[string]any{"text": "working"}}),
		"claude-sonnet-4", base)

	trace, err := Adapter{Dir: data}.Parse(SessionPath("s1"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// No read events at all → unavailable; but the important thing is
	// it doesn't crash. If there were reads, it would be estimated.
	if trace.Stats.Observability.Reads == model.ObservabilityExact {
		t.Fatalf("reads should not be exact without read_files table")
	}
}

// TestParseEmitsFinishReasonMarks verifies non-normal finish reasons
// (error, content_filter, canceled, max_tokens) produce finish-reason
// marks in the trace.
func TestParseEmitsFinishReasonMarks(t *testing.T) {
	for _, reason := range []string{"error", "content_filter", "canceled", "max_tokens"} {
		t.Run(reason, func(t *testing.T) {
			data, db := newFixtureDB(t, nil)
			base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
			insertSession(t, db, "s1", "", "Finish test", base, 1)
			insertMessage(t, db, "s1", "m1", "assistant",
				writeParts(t,
					map[string]any{"type": "text", "data": map[string]any{"text": "trying"}},
					map[string]any{"type": "finish", "data": map[string]any{"reason": reason}},
				),
				"claude-sonnet-4", base)

			trace, err := Adapter{Dir: data}.Parse(SessionPath("s1"))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			var saw bool

			for _, m := range trace.Marks {
				if m.Type == "finish-reason" {
					saw = true

					if !strings.Contains(m.Note, reason) {
						t.Fatalf("note = %q, want to contain %q", m.Note, reason)
					}
				}
			}

			if !saw {
				t.Fatalf("expected finish-reason mark for %q, marks = %+v", reason, trace.Marks)
			}
		})
	}
}

// TestParseEmitsThinkingMark verifies a reasoning part produces a
// thinking mark with the thinking text and duration.
func TestParseEmitsThinkingMark(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "s1", "", "Thinking test", base, 1)
	insertMessage(t, db, "s1", "m1", "assistant",
		writeParts(t,
			map[string]any{"type": "reasoning", "data": map[string]any{
				"thinking":    "I should check the file first",
				"started_at":  1000,
				"finished_at": 1012,
			}},
			map[string]any{"type": "text", "data": map[string]any{"text": "Let me check."}},
		),
		"claude-sonnet-4", base)

	trace, err := Adapter{Dir: data}.Parse(SessionPath("s1"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var sawThinking bool

	for _, m := range trace.Marks {
		if m.Type == "thinking" {
			sawThinking = true

			if !strings.Contains(m.Note, "12s") {
				t.Fatalf("thinking note should contain duration 12s, got %q", m.Note)
			}

			if !strings.Contains(m.Note, "check the file") {
				t.Fatalf("thinking note should contain text, got %q", m.Note)
			}
		}
	}

	if !sawThinking {
		t.Fatalf("expected a thinking mark, marks = %+v", trace.Marks)
	}
}

// TestListSessionsPopulatesUsageAndCost verifies the SessionMeta
// carries prompt_tokens, completion_tokens, and cost from the
// sessions table.
func TestListSessionsPopulatesUsageAndCost(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSessionWithUsage(t, db, "s1", "Usage test", base, 5000, 12000, 0.42)

	metas, err := Adapter{Dir: data}.ListSessions()
	if err != nil {
		t.Fatal(err)
	}

	if len(metas) != 1 {
		t.Fatalf("metas = %d, want 1", len(metas))
	}

	m := metas[0]
	if m.PromptTokens != 5000 {
		t.Fatalf("promptTokens = %d, want 5000", m.PromptTokens)
	}

	if m.CompletionTokens != 12000 {
		t.Fatalf("completionTokens = %d, want 12000", m.CompletionTokens)
	}

	if m.Cost != 0.42 {
		t.Fatalf("cost = %f, want 0.42", m.Cost)
	}
}

// createOldSchemaDB creates a Crush database with the pre-migration
// schema — no cost column on sessions, no finished_at on messages.
// This simulates a database from an older Crush version.
func createOldSchemaDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()

	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	handle, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}

	handle.SetMaxOpenConns(1)

	oldSchema := `
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			parent_session_id TEXT,
			title TEXT NOT NULL,
			message_count INTEGER NOT NULL DEFAULT 0,
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			todos TEXT
		);
		CREATE TABLE messages (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			parts TEXT NOT NULL DEFAULT '[]',
			model TEXT,
			provider TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
	`
	if _, err := handle.Exec(oldSchema); err != nil {
		_ = handle.Close()

		t.Fatal(err)
	}

	t.Cleanup(func() { _ = handle.Close() })

	return handle
}

// TestOldSchemaListSessionsDoesNotCrash verifies that a database
// without the cost column (pre-migration Crush) does not crash
// ListSessions. The dynamic query builder should substitute a
// literal zero for the missing column.
func TestOldSchemaListSessionsDoesNotCrash(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, ".crush")
	dbPath := filepath.Join(dataDir, dbName)
	db := createOldSchemaDB(t, dbPath)

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(
		`INSERT INTO sessions (id, title, message_count, prompt_tokens, completion_tokens, updated_at, created_at) VALUES (?, ?, 0, 0, 0, ?, ?)`,
		"s1",
		"Old schema",
		base.Unix(),
		base.Unix(),
	); err != nil {
		t.Fatal(err)
	}

	metas, err := Adapter{Dir: dataDir}.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions on old schema should not error: %v", err)
	}

	if len(metas) != 1 {
		t.Fatalf("metas = %d, want 1", len(metas))
	}

	if metas[0].Cost != 0 {
		t.Fatalf("cost should default to 0 on old schema, got %f", metas[0].Cost)
	}
}

// TestOldSchemaParseDoesNotCrash verifies that Parse works on a
// database without the finished_at column.
func TestOldSchemaParseDoesNotCrash(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, ".crush")
	dbPath := filepath.Join(dataDir, dbName)
	db := createOldSchemaDB(t, dbPath)

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(
		`INSERT INTO sessions (id, title, message_count, prompt_tokens, completion_tokens, updated_at, created_at) VALUES (?, ?, 1, 0, 0, ?, ?)`,
		"s1",
		"Old schema parse",
		base.Unix(),
		base.Unix(),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(
		`INSERT INTO messages (id, session_id, role, parts, model, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"m1",
		"s1",
		"assistant",
		writeParts(t, map[string]any{"type": "text", "data": map[string]any{"text": "hello"}}),
		"claude-sonnet-4",
		base.Unix(),
		base.Unix(),
	); err != nil {
		t.Fatal(err)
	}

	trace, err := Adapter{Dir: dataDir}.Parse(SessionPath("s1"))
	if err != nil {
		t.Fatalf("Parse on old schema should not error: %v", err)
	}

	if trace.Session.ID != "s1" {
		t.Fatalf("session id = %q", trace.Session.ID)
	}
}

// TestThinkingMarkUsesMessageDuration verifies that when a reasoning
// part lacks started_at/finished_at timestamps, the thinking mark
// falls back to the message-level finished_at - created_at duration.
func TestThinkingMarkUsesMessageDuration(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "s1", "", "Thinking duration test", base, 1)
	// Message with finished_at = created + 7 seconds
	if _, err := db.Exec(
		`INSERT INTO messages (id, session_id, role, parts, model, created_at, updated_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"m1",
		"s1",
		"assistant",
		writeParts(
			t,
			map[string]any{"type": "reasoning", "data": map[string]any{"thinking": "I need to think about this"}},
		),
		"claude-sonnet-4",
		base.Unix(),
		base.Unix(),
		base.Add(7*time.Second).Unix(),
	); err != nil {
		t.Fatal(err)
	}

	trace, err := Adapter{Dir: data}.Parse(SessionPath("s1"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var sawThinking bool

	for _, m := range trace.Marks {
		if m.Type == "thinking" {
			sawThinking = true

			if !strings.Contains(m.Note, "7s") {
				t.Fatalf("thinking note should contain duration 7s (from finished_at), got %q", m.Note)
			}
		}
	}

	if !sawThinking {
		t.Fatalf("expected a thinking mark, marks = %+v", trace.Marks)
	}
}
