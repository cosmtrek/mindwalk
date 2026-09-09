package crush

import (
	"path/filepath"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/model"
)

// fixtureDir is the path to the committed testdata/crush/crush.db
// fixture. The fixture is built once by the gencrushfixture helper
// (not committed) and checked into git so CI reproduces the
// end-to-end Crush verification path without a host install.
func fixtureDir(t *testing.T) string {
	t.Helper()

	return filepath.Join("..", "..", "..", "testdata", "crush")
}

// TestFixtureLoadsAllSessions walks the committed fixture and
// verifies both the root session and the auxiliary agent session
// surface through ListSessions with the expected metadata.
func TestFixtureLoadsAllSessions(t *testing.T) {
	dir := fixtureDir(t)

	metas, err := Adapter{Dir: dir}.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if len(metas) != 1 {
		t.Fatalf("metas = %d, want 1 (child sessions should be hidden)", len(metas))
	}

	root := metas[0]
	if root.ID != "fixture-root" {
		t.Fatalf("id = %q", root.ID)
	}

	if root.Harness != "crush" {
		t.Fatalf("harness = %q", root.Harness)
	}

	if root.Title != "Fixture root session" {
		t.Fatalf("title = %q", root.Title)
	}

	if !IsSessionPath(root.Path) {
		t.Fatalf("path = %q, want crush://session/<id>", root.Path)
	}
}

// TestFixtureParsesTraceAsExpected decodes the root session into a
// Trace and checks the event count, model field, and the marks
// (user-message + subagent). This is the same path the server's
// /api/sessions/<key>/trace endpoint takes.
func TestFixtureParsesTraceAsExpected(t *testing.T) {
	dir := fixtureDir(t)

	trace, err := Adapter{Dir: dir}.Parse(SessionPath("fixture-root"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if trace.Session.Harness != "crush" {
		t.Fatalf("harness = %q", trace.Session.Harness)
	}

	if trace.Session.Model != "minimax/minimax-m3" {
		t.Fatalf("model = %q", trace.Session.Model)
	}
	// Four events: agent (subagent), read, write, bash.
	if len(trace.Events) != 4 {
		t.Fatalf("events = %d, want 4 (agent + read + write + bash)", len(trace.Events))
	}

	toolNames := make(map[string]bool)
	for _, ev := range trace.Events {
		toolNames[ev.Tool] = true
	}

	for _, want := range []string{"agent", "read", "write", "bash"} {
		if !toolNames[want] {
			t.Fatalf("missing %q event in trace; tools present: %v", want, toolNames)
		}
	}
	// Three marks: user-message (first turn), subagent (agent call),
	// and user-message (second turn with finish/stop).
	if len(trace.Marks) != 3 {
		t.Fatalf("marks = %+v", trace.Marks)
	}

	sawUser, sawSub := 0, false

	for _, m := range trace.Marks {
		switch m.Type {
		case "user-message":
			sawUser++
		case "subagent":
			sawSub = true

			if m.Note != "read server" {
				t.Fatalf("subagent note = %q", m.Note)
			}
		}
	}

	if sawUser != 2 {
		t.Fatalf("expected 2 user-message marks, got %d", sawUser)
	}

	if !sawSub {
		t.Fatalf("expected a subagent mark")
	}
}

// TestFixtureFileTouchingEventsHaveTargets verifies the enriched
// fixture's read, write, and bash tool calls each produce trace events
// with tool names set correctly — exercising the path the server's
// trace endpoint and citymap builder consume.
func TestFixtureFileTouchingEventsHaveTargets(t *testing.T) {
	dir := fixtureDir(t)

	trace, err := Adapter{Dir: dir}.Parse(SessionPath("fixture-root"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	byTool := map[string]model.Event{}
	for _, ev := range trace.Events {
		byTool[ev.Tool] = ev
	}

	for _, want := range []string{"read", "write", "bash"} {
		ev, ok := byTool[want]
		if !ok {
			t.Fatalf("missing %q event", want)
		}

		if ev.Seq < 0 {
			t.Fatalf("%q event has negative seq: %d", want, ev.Seq)
		}
	}
	// The read event should reference a file path via an outside
	// touch (the fixture has no Cwd, so absolute paths land outside).
	readEv := byTool["read"]
	if len(readEv.Outside) == 0 && len(readEv.Targets) == 0 {
		t.Fatalf("read event has no targets or outside touches: %+v", readEv)
	}
	// The write event should also touch a file.
	writeEv := byTool["write"]
	if len(writeEv.Outside) == 0 && len(writeEv.Targets) == 0 {
		t.Fatalf("write event has no targets or outside touches: %+v", writeEv)
	}
}

// TestFixtureSummarizesChildSession ensures the auxiliary session
// is reachable via Summarize with the correct SourceID and
// LaunchCallID parsed from the `$$` separator.
func TestFixtureSummarizesChildSession(t *testing.T) {
	dir := fixtureDir(t)

	meta, err := Adapter{Dir: dir}.Summarize(SessionPath("m_assistant_1$$call_agent_1"))
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	if !meta.Auxiliary {
		t.Fatalf("Auxiliary = false, want true")
	}

	if meta.Agent == nil {
		t.Fatalf("Agent meta nil")
	}

	if meta.Agent.SourceID != "m_assistant_1" {
		t.Fatalf("SourceID = %q", meta.Agent.SourceID)
	}

	if meta.Agent.LaunchCallID != "call_agent_1" {
		t.Fatalf("LaunchCallID = %q", meta.Agent.LaunchCallID)
	}

	if meta.Agent.RootSessionID != "fixture-root" {
		t.Fatalf("RootSessionID = %q", meta.Agent.RootSessionID)
	}
}

// TestFixtureBuildsAgentGraph verifies the agent graph builder
// emits the main node plus the auxiliary child node when given the
// fixture's catalog.
func TestFixtureBuildsAgentGraph(t *testing.T) {
	dir := fixtureDir(t)
	adapter := Adapter{Dir: dir}

	metas, err := adapter.ListSessions()
	if err != nil {
		t.Fatal(err)
	}

	childMeta, err := adapter.Summarize(SessionPath("m_assistant_1$$call_agent_1"))
	if err != nil {
		t.Fatal(err)
	}

	root := metas[0]

	graph, err := adapter.BuildAgentGraph(root, []model.SessionMeta{childMeta})
	if err != nil {
		t.Fatalf("BuildAgentGraph: %v", err)
	}

	if graph.RootSessionKey != root.Key {
		t.Fatalf("root key = %q", graph.RootSessionKey)
	}

	if len(graph.Agents) != 2 {
		t.Fatalf("agents = %d, want 2 (main + child)", len(graph.Agents))
	}

	var sub *model.AgentNode

	for i := range graph.Agents {
		if graph.Agents[i].Kind == model.AgentKindSubagent {
			sub = &graph.Agents[i]
		}
	}

	if sub == nil {
		t.Fatalf("no subagent node in graph: %+v", graph.Agents)
	}

	if sub.LinkQuality != model.AgentLinkQualityExact {
		t.Fatalf("link quality = %q, want exact", sub.LinkQuality)
	}

	if sub.TraceSessionKey != childMeta.Key {
		t.Fatalf("trace key = %q, want %q", sub.TraceSessionKey, childMeta.Key)
	}
}

// TestFixtureTokenEconomics verifies the fixture carries non-zero
// token and cost values so the exact-observability code path is
// exercised end-to-end.
func TestFixtureTokenEconomics(t *testing.T) {
	dir := fixtureDir(t)

	metas, err := Adapter{Dir: dir}.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if len(metas) != 1 {
		t.Fatalf("metas = %d, want 1", len(metas))
	}

	root := metas[0]
	if root.PromptTokens == 0 {
		t.Error("PromptTokens = 0, want non-zero")
	}

	if root.CompletionTokens == 0 {
		t.Error("CompletionTokens = 0, want non-zero")
	}

	if root.Cost == 0 {
		t.Error("Cost = 0, want non-zero")
	}
}

// TestFixtureReadObservability verifies the read_files table in the
// fixture upgrades the read observability grade from estimated to exact.
func TestFixtureReadObservability(t *testing.T) {
	dir := fixtureDir(t)

	trace, err := Adapter{Dir: dir}.Parse(SessionPath("fixture-root"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if trace.Stats.Observability.Reads != model.ObservabilityExact {
		t.Fatalf("reads observability = %q, want exact (read_files table should upgrade it)",
			trace.Stats.Observability.Reads)
	}
}

// TestFixtureErrorObservability pins the regression for T06: the
// crush adapter must mark every observed tool_result as
// OutcomeKnown so the error observability grade becomes exact when
// no event was left unmatched. ComputeStats will auto-degrade back
// to estimated the moment any event carries OutcomeKnown=false,
// which acts as a tripwire for future schema drift.
func TestFixtureErrorObservability(t *testing.T) {
	t.Parallel()

	dir := fixtureDir(t)

	trace, err := Adapter{Dir: dir}.Parse(SessionPath("fixture-root"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for i, ev := range trace.Events {
		if !ev.IsError && !ev.OutcomeKnown {
			t.Errorf("event[%d] (%s) has unknown outcome; parts.go should mark observed results as known",
				i, ev.Tool)
		}
	}

	if trace.Stats.Observability.Errors != model.ObservabilityExact {
		t.Fatalf("errors observability = %q, want exact (every observed result is OutcomeKnown)",
			trace.Stats.Observability.Errors)
	}
}

// BenchmarkFixtureListSessions measures the cold-open + session-listing
// path. Each iteration opens the SQLite file read-only, runs the
// sessions query, and closes the handle.
func BenchmarkFixtureListSessions(b *testing.B) {
	dir := filepath.Join("..", "..", "..", "testdata", "crush")
	a := Adapter{Dir: dir}

	b.ReportAllocs()

	for b.Loop() {
		if _, err := a.ListSessions(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFixtureParse measures the full trace-parse path: open DB,
// walk every message row, decode parts JSON, build events/marks,
// close. This is the path /api/sessions/<key>/trace exercises.
func BenchmarkFixtureParse(b *testing.B) {
	dir := filepath.Join("..", "..", "..", "testdata", "crush")
	a := Adapter{Dir: dir}
	path := SessionPath("fixture-root")

	b.ReportAllocs()

	for b.Loop() {
		if _, err := a.Parse(path); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFixtureBuildAgentGraph measures the cold + warm graph
// build path: ListSessions (for the catalog), then BuildAgentGraph.
// This is the path /api/sessions/<key>/agent-graph exercises. v0.3.0
// replaced the recursive CTE with a single WITH RECURSIVE — keep
// this benchmark as the regression baseline for that change.
func BenchmarkFixtureBuildAgentGraph(b *testing.B) {
	dir := filepath.Join("..", "..", "..", "testdata", "crush")
	a := Adapter{Dir: dir}

	catalog, err := a.ListSessions()
	if err != nil {
		b.Fatal(err)
	}

	if len(catalog) == 0 {
		b.Skip("fixture catalog is empty — nothing to graph")
	}

	root := catalog[0]

	b.ReportAllocs()

	for b.Loop() {
		if _, err := a.BuildAgentGraph(root, catalog); err != nil {
			b.Fatal(err)
		}
	}
}
