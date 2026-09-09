package crush

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/cosmtrek/mindwalk/internal/model"
)

// TestBuildAgentGraphExactMatch verifies the happy path: a launched
// `agent` tool call whose result contains an agent_id matches the
// child row in the catalog, and the resulting graph node is tagged
// `LinkQuality=exact` with `TraceAvailability=available`.
func TestBuildAgentGraphExactMatch(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	// Root session that spawns an agent via the `agent` tool.
	insertSession(t, db, "root", "", "Root", base, 1)

	launchID := "agent_call_1"
	childMsgID := "msg_child_1"
	insertMessage(t, db, "root", "msg_root_1", "assistant", writeParts(t,
		map[string]any{"type": "tool_call", "data": map[string]any{
			"id": launchID, "name": "agent",
			"input":    `{"task_title":"explore","agent_type":"explore","message":"read the schema"}`,
			"finished": true,
		}},
		map[string]any{"type": "tool_result", "data": map[string]any{
			"tool_call_id": launchID,
			"name":         "agent",
			"content":      `{"agent_id":"` + childMsgID + `","nickname":"explore","task_name":"explore"}`,
		}},
	), "", base)

	// Child session whose id encodes the parent message id and the
	// launching tool call id, matching the format Crush uses.
	childID := childMsgID + "$$" + launchID
	insertSession(t, db, childID, "root", "Agent: explore", base.Add(time.Second), 2)

	root := model.SessionMeta{
		Key:        SessionKey("root"),
		ID:         "root",
		Harness:    "crush",
		Path:       SessionPath("root"),
		EventCount: 1,
	}

	childMeta, err := Adapter{Dir: data}.Summarize(SessionPath(childID))
	if err != nil {
		t.Fatalf("summarize child: %v", err)
	}

	graph, err := Adapter{Dir: data}.BuildAgentGraph(root, []model.SessionMeta{childMeta})
	if err != nil {
		t.Fatalf("BuildAgentGraph: %v", err)
	}

	if graph.RootSessionKey != root.Key {
		t.Fatalf("root key = %q", graph.RootSessionKey)
	}

	if len(graph.Agents) != 2 {
		t.Fatalf("agents = %d, want 2 (main + exact child)", len(graph.Agents))
	}

	var sub *model.AgentNode

	for i := range graph.Agents {
		if graph.Agents[i].Kind == model.AgentKindSubagent {
			sub = &graph.Agents[i]

			break
		}
	}

	if sub == nil {
		t.Fatalf("no subagent node in graph: %+v", graph.Agents)
	}

	if sub.LinkQuality != model.AgentLinkQualityExact {
		t.Fatalf("link quality = %q, want exact", sub.LinkQuality)
	}

	if sub.TraceAvailability != model.TraceAvailabilityAvailable {
		t.Fatalf("trace availability = %q, want available", sub.TraceAvailability)
	}

	if sub.TraceSessionKey != childMeta.Key {
		t.Fatalf("trace session key = %q, want %q", sub.TraceSessionKey, childMeta.Key)
	}

	if sub.LaunchCallID != launchID {
		t.Fatalf("launch call id = %q", sub.LaunchCallID)
	}

	if sub.InstructionPreview == "" {
		t.Fatalf("instruction preview should be populated from message arg")
	}
}

// TestBuildAgentGraphUnlinkedLaunch covers the branch where the
// agent tool returned a non-JSON or empty result: the launch is
// surfaced as an `unavailable` node with the launching tool call id
// as the label.
func TestBuildAgentGraphUnlinkedLaunch(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	insertSession(t, db, "root", "", "Root", base, 1)

	launchID := "agent_call_no_match"
	insertMessage(t, db, "root", "msg_root_1", "assistant", writeParts(t,
		map[string]any{"type": "tool_call", "data": map[string]any{
			"id": launchID, "name": "agent",
			"input":    `{"task_title":"explore","agent_type":"explore","message":"read"}`,
			"finished": true,
		}},
		// Garbage result — not the expected JSON shape with
		// agent_id, so the launch goes into the unlinked branch.
		map[string]any{"type": "tool_result", "data": map[string]any{
			"tool_call_id": launchID,
			"name":         "agent",
			"content":      "this is not the expected json shape",
		}},
	), "", base)

	root := model.SessionMeta{Key: SessionKey("root"), ID: "root", Harness: "crush", Path: SessionPath("root")}

	graph, err := Adapter{Dir: data}.BuildAgentGraph(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(graph.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(graph.Agents))
	}

	var sub *model.AgentNode

	for i := range graph.Agents {
		if graph.Agents[i].Kind == model.AgentKindSubagent {
			sub = &graph.Agents[i]
		}
	}

	if sub == nil {
		t.Fatalf("no subagent node")
	}

	if sub.LinkQuality != model.AgentLinkQualityUnavailable {
		t.Fatalf("link quality = %q, want unavailable", sub.LinkQuality)
	}

	if sub.TraceAvailability != model.TraceAvailabilityMissing {
		t.Fatalf("trace availability = %q, want missing", sub.TraceAvailability)
	}

	if sub.Status != model.AgentStatusFailed {
		t.Fatalf("status = %q, want failed (garbage output)", sub.Status)
	}
}

// TestBuildAgentGraphDerivedChild covers the branch where a child
// session exists in the catalog but no matching agent launch was
// observed in the root session's messages. The child still surfaces
// in the graph with LinkQuality=derived so the user can investigate.
func TestBuildAgentGraphDerivedChild(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	insertSession(t, db, "root", "", "Root", base, 1)
	// Root has only a non-agent tool call, so no launches are read.
	insertMessage(t, db, "root", "msg_root_1", "assistant", writeParts(t,
		map[string]any{"type": "tool_call", "data": map[string]any{
			"id": "view_1", "name": "view",
			"input": `{"file_path":"foo.go"}`, "finished": true,
		}},
	), "", base)

	// A child whose parent_session_id is "root" but whose id uses
	// the agent-tool format with a launch id that was never recorded.
	childID := "msg_some_old$$orphan_call"
	insertSession(t, db, childID, "root", "Agent: orphan", base.Add(time.Second), 1)

	childMeta, err := Adapter{Dir: data}.Summarize(SessionPath(childID))
	if err != nil {
		t.Fatal(err)
	}

	root := model.SessionMeta{Key: SessionKey("root"), ID: "root", Harness: "crush", Path: SessionPath("root")}

	graph, err := Adapter{Dir: data}.BuildAgentGraph(root, []model.SessionMeta{childMeta})
	if err != nil {
		t.Fatal(err)
	}

	var sub *model.AgentNode

	for i := range graph.Agents {
		if graph.Agents[i].Kind == model.AgentKindSubagent {
			sub = &graph.Agents[i]
		}
	}

	if sub == nil {
		t.Fatalf("expected derived subagent node, got %+v", graph.Agents)
	}

	if sub.LinkQuality != model.AgentLinkQualityDerived {
		t.Fatalf("link quality = %q, want derived", sub.LinkQuality)
	}

	if sub.TraceAvailability != model.TraceAvailabilityAvailable {
		t.Fatalf("trace availability = %q, want available", sub.TraceAvailability)
	}

	if sub.LaunchCallID != "orphan_call" {
		t.Fatalf("launch call id = %q, want orphan_call", sub.LaunchCallID)
	}
}

// TestBuildAgentGraphEmptyCatalog verifies the graph contains only
// the main node when no children are present in the catalog.
func TestBuildAgentGraphEmptyCatalog(t *testing.T) {
	data, db := newFixtureDB(t, nil)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	insertSession(t, db, "root", "", "Root", base, 1)
	insertMessage(t, db, "root", "msg_root_1", "assistant", writeParts(t,
		map[string]any{"type": "text", "data": map[string]any{"text": "Just thinking."}},
	), "", base)

	root := model.SessionMeta{Key: SessionKey("root"), ID: "root", Harness: "crush", Path: SessionPath("root")}

	graph, err := Adapter{Dir: data}.BuildAgentGraph(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(graph.Agents) != 1 {
		t.Fatalf("agents = %d, want 1 (main only)", len(graph.Agents))
	}

	if graph.Agents[0].Kind != model.AgentKindMain {
		t.Fatalf("kind = %q", graph.Agents[0].Kind)
	}
}

// TestAgentGraphInputsReturnsRootPath verifies the inputs helper
// surfaces the root session path. Tests that want a deterministic
// catalog without touching the host filesystem use a t.TempDir.
func TestAgentGraphInputsReturnsRootPath(t *testing.T) {
	root := model.SessionMeta{Key: "k", ID: "i", Harness: "crush", Path: SessionPath("xyz")}

	got, err := Adapter{Dir: filepath.Join(t.TempDir(), "no-crush")}.AgentGraphInputs(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0] != root.Path {
		t.Fatalf("inputs = %v, want [%q]", got, root.Path)
	}
	// Empty root path: no path to discover, but the inputs helper
	// must not crash. It returns whatever auxiliary paths the
	// catalog/database had — here, none.
	empty, _ := Adapter{Dir: filepath.Join(t.TempDir(), "no-crush")}.AgentGraphInputs(model.SessionMeta{}, nil)
	for _, p := range empty {
		if p != "" {
			t.Fatalf("empty root path should produce no non-empty inputs, got %v", empty)
		}
	}
}

// TestIndexChildrenByParentGroupsCrushAuxiliaries verifies the index
// helper groups auxiliary sessions by their parent_session_id and
// ignores sessions from other harnesses.
func TestIndexChildrenByParentGroupsCrushAuxiliaries(t *testing.T) {
	catalog := []model.SessionMeta{
		{Key: "a", Harness: "crush", Agent: &model.AgentSessionMeta{RootSessionID: "p1", LaunchCallID: "c1"}},
		{Key: "b", Harness: "crush", Agent: &model.AgentSessionMeta{RootSessionID: "p1", LaunchCallID: "c2"}},
		{Key: "c", Harness: "crush", Agent: &model.AgentSessionMeta{RootSessionID: "p2", LaunchCallID: "c3"}},
		{Key: "d", Harness: "claudecode", Agent: &model.AgentSessionMeta{RootSessionID: "p1", LaunchCallID: "ignored"}},
		{Key: "e", Harness: "crush"}, // no Agent metadata -> ignored
	}

	idx := indexChildrenByParent(catalog)
	if len(idx["p1"]) != 2 {
		t.Fatalf("p1 children = %d, want 2", len(idx["p1"]))
	}

	if len(idx["p2"]) != 1 {
		t.Fatalf("p2 children = %d, want 1", len(idx["p2"]))
	}

	if idx["p1"][0].Key != "a" || idx["p1"][1].Key != "b" {
		t.Fatalf("p1 not sorted: %v", idx["p1"])
	}
}

// TestParseCrushLaunchOutputCoversBranches verifies the small helper
// that extracts agent_id / nickname / task_name from the result
// JSON behaves as expected across the supported shapes.
func TestParseCrushLaunchOutputCoversBranches(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantOK    bool
		wantAgent string
	}{
		{"all_fields", `{"agent_id":"a1","nickname":"nick","task_name":"t"}`, true, "a1"},
		{"agent_id_only", `{"agent_id":"a1"}`, true, "a1"},
		{"empty_object", `{}`, false, ""},
		{"empty_string", "", false, ""},
		{"non_json", "not json", false, ""},
		{"nickname_only", `{"nickname":"n"}`, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCrushLaunchOutput(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}

			if ok && got.AgentID != tt.wantAgent {
				t.Fatalf("agent_id = %q, want %q", got.AgentID, tt.wantAgent)
			}
		})
	}
}

// TestCrushChildDepthPrefersRecorded verifies the depth helper
// uses a child's recorded depth when set, otherwise defaults to
// parent+1.
func TestCrushChildDepthPrefersRecorded(t *testing.T) {
	if got := crushChildDepth(3, 1); got != 3 {
		t.Fatalf("recorded wins: got %d", got)
	}

	if got := crushChildDepth(0, 2); got != 3 {
		t.Fatalf("fallback parent+1: got %d", got)
	}

	if got := crushChildDepth(-1, 2); got != 3 {
		t.Fatalf("negative recorded falls back: got %d", got)
	}
}

// TestAgentArgumentReadsAlias verifies the helper falls back from
// the canonical key through the conventional aliases.
func TestAgentArgumentReadsAlias(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		key   string
		want  string
	}{
		{"primary", map[string]any{"agent_type": "explore"}, "agent_type", "explore"},
		{"task_title", map[string]any{"task_title": "review"}, "agent_type", "review"},
		{"title", map[string]any{"title": "investigate"}, "agent_type", "investigate"},
		{"description", map[string]any{"description": "look closer"}, "agent_type", "look closer"},
		{"message", map[string]any{"message": "deep dive"}, "agent_type", "deep dive"},
		{"missing", map[string]any{}, "agent_type", ""},
		{"non_string", map[string]any{"agent_type": 42}, "agent_type", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := agentArgument(c.input, c.key)
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
