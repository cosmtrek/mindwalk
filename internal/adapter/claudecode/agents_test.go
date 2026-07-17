package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	baseadapter "github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestClaudeAgentGraphExactUsesToolUseIDForImmediateParent(t *testing.T) {
	fixture := newClaudeAgentFixture(t, []any{
		claudeToolUse("root-id", "call-read", "Read", map[string]any{"file_path": "/tmp/a.go"}),
		claudeToolResult("root-id", "call-read", false),
		claudeToolUse("root-id", "call-child", "Agent", map[string]any{
			"description":   "Launch description",
			"subagent_type": "Explore",
			"prompt":        "  inspect\n  the code  ",
		}),
		claudeToolResult("root-id", "call-child", false),
	})
	child := fixture.addChild(t, "agent-child", []any{
		claudeSidechainUser("root-id", "child-id", "child prompt"),
		claudeToolUseForAgent("root-id", "child-id", "call-grand", "Task", map[string]any{
			"description":   "Grand launch",
			"subagent_type": "Plan",
			"prompt":        "plan the fix",
		}),
		claudeToolResultForAgent("root-id", "child-id", "call-grand", false),
	}, claudeChildMeta{AgentType: "Explore", Description: "Child", ToolUseID: "call-child", SpawnDepth: 1})
	grand := fixture.addChild(t, "agent-grand", []any{
		claudeSidechainUser("root-id", "grand-id", "grand prompt"),
	}, claudeChildMeta{AgentType: "Plan", Description: "Grandchild", ToolUseID: "call-grand", SpawnDepth: 2})

	graph := fixture.build(t)
	if graph.Version != model.AgentGraphVersion || graph.RootSessionKey != fixture.root.Key {
		t.Fatalf("graph header = %#v", graph)
	}
	if len(graph.Agents) != 3 || graph.Agents[0] != claudeMainNode(fixture.root) {
		t.Fatalf("agents = %#v", graph.Agents)
	}
	childNode := findClaudeAgent(t, graph, "Child")
	childID := claudeAgentID(fixture.root, "claude-tool:call-child")
	assertClaudeAgent(t, childNode, model.AgentNode{
		ID:                 childID,
		ParentID:           claudeMainID(fixture.root),
		Depth:              1,
		Kind:               model.AgentKindSubagent,
		Label:              "Child",
		Role:               "Explore",
		InstructionPreview: "inspect the code",
		LaunchSeq:          intPointer(1),
		LaunchCallID:       "call-child",
		Status:             model.AgentStatusLaunched,
		TraceAvailability:  model.TraceAvailabilityAvailable,
		TraceSessionKey:    child.Key,
		TraceEventCount:    1,
		LinkQuality:        model.AgentLinkQualityExact,
		LinkMethod:         model.AgentLinkMethodClaudeToolUseID,
	})
	assertClaudeAgent(t, findClaudeAgent(t, graph, "Grandchild"), model.AgentNode{
		ID:                 claudeAgentID(fixture.root, "claude-tool:call-grand"),
		ParentID:           childID,
		Depth:              2,
		Kind:               model.AgentKindSubagent,
		Label:              "Grandchild",
		Role:               "Plan",
		InstructionPreview: "plan the fix",
		LaunchSeq:          intPointer(0),
		LaunchCallID:       "call-grand",
		Status:             model.AgentStatusLaunched,
		TraceAvailability:  model.TraceAvailabilityAvailable,
		TraceSessionKey:    grand.Key,
		TraceEventCount:    0,
		LinkQuality:        model.AgentLinkQualityExact,
		LinkMethod:         model.AgentLinkMethodClaudeToolUseID,
	})
}

func TestClaudeAgentGraphDerivedArtifactWithoutToolUseIDIsLaunched(t *testing.T) {
	fixture := newClaudeAgentFixture(t, nil)
	child := fixture.addChild(t, "agent-derived", []any{
		claudeSidechainUser("root-id", "derived-id", "inspect"),
	}, claudeChildMeta{AgentType: "Explore", Description: "Derived", SpawnDepth: 1})

	node := findClaudeAgent(t, fixture.build(t), "Derived")
	assertClaudeAgent(t, node, model.AgentNode{
		ID:                claudeAgentID(fixture.root, "session:"+child.Key),
		ParentID:          claudeMainID(fixture.root),
		Depth:             1,
		Kind:              model.AgentKindSubagent,
		Label:             "Derived",
		Role:              "Explore",
		Status:            model.AgentStatusLaunched,
		TraceAvailability: model.TraceAvailabilityAvailable,
		TraceSessionKey:   child.Key,
		TraceEventCount:   0,
		LinkQuality:       model.AgentLinkQualityDerived,
		LinkMethod:        model.AgentLinkMethodClaudeSubagentsDirectory,
	})
}

func TestClaudeAgentGraphUnmatchedMetadataAttachesToMain(t *testing.T) {
	fixture := newClaudeAgentFixture(t, []any{
		claudeToolUse("root-id", "call-other", "Agent", map[string]any{"prompt": "other"}),
	})
	child := fixture.addChild(t, "agent-orphan", []any{
		claudeSidechainUser("root-id", "orphan-id", "inspect"),
	}, claudeChildMeta{AgentType: "Explore", Description: "Orphan", ToolUseID: "call-unmatched", SpawnDepth: 2})

	node := findClaudeAgent(t, fixture.build(t), "Orphan")
	assertClaudeAgent(t, node, model.AgentNode{
		ID:                claudeAgentID(fixture.root, "claude-tool:call-unmatched"),
		ParentID:          claudeMainID(fixture.root),
		Depth:             2,
		Kind:              model.AgentKindSubagent,
		Label:             "Orphan",
		Role:              "Explore",
		Status:            model.AgentStatusLaunched,
		TraceAvailability: model.TraceAvailabilityAvailable,
		TraceSessionKey:   child.Key,
		TraceEventCount:   0,
		LinkQuality:       model.AgentLinkQualityDerived,
		LinkMethod:        model.AgentLinkMethodClaudeSubagentsDirectory,
	})
}

func TestClaudeAgentGraphMetadataOnlyChildIsMissing(t *testing.T) {
	fixture := newClaudeAgentFixture(t, []any{
		claudeToolUse("root-id", "call-missing", "Task", map[string]any{
			"description":   "Missing launch",
			"subagent_type": "Explore",
			"prompt":        "find the missing trace",
		}),
	})
	fixture.addMetaOnly(t, "agent-missing", claudeChildMeta{
		AgentType: "Explore", Description: "Missing", ToolUseID: "call-missing", SpawnDepth: 1,
	})

	node := findClaudeAgent(t, fixture.build(t), "Missing")
	assertClaudeAgent(t, node, model.AgentNode{
		ID:                 claudeAgentID(fixture.root, "claude-tool:call-missing"),
		ParentID:           claudeMainID(fixture.root),
		Depth:              1,
		Kind:               model.AgentKindSubagent,
		Label:              "Missing",
		Role:               "Explore",
		InstructionPreview: "find the missing trace",
		LaunchSeq:          intPointer(0),
		LaunchCallID:       "call-missing",
		Status:             model.AgentStatusLaunched,
		TraceAvailability:  model.TraceAvailabilityMissing,
		LinkQuality:        model.AgentLinkQualityExact,
		LinkMethod:         model.AgentLinkMethodClaudeToolUseID,
	})
}

func TestClaudeAgentGraphZeroEventChildIsAvailable(t *testing.T) {
	fixture := newClaudeAgentFixture(t, []any{
		claudeToolUse("root-id", "call-zero", "Agent", map[string]any{"prompt": "zero events"}),
	})
	child := fixture.addChild(t, "agent-zero", []any{
		map[string]any{"type": "session_meta", "sessionId": "root-id", "agentId": "zero-id", "isSidechain": true},
		claudeSidechainUser("root-id", "zero-id", "user only"),
	}, claudeChildMeta{Description: "Zero", ToolUseID: "call-zero", SpawnDepth: 1})

	node := findClaudeAgent(t, fixture.build(t), "Zero")
	if node.TraceAvailability != model.TraceAvailabilityAvailable || node.TraceSessionKey != child.Key || node.TraceEventCount != 0 {
		t.Fatalf("zero-event node = %#v", node)
	}
}

func TestClaudeAgentGraphErrorResultIsFailed(t *testing.T) {
	fixture := newClaudeAgentFixture(t, []any{
		claudeToolUse("root-id", "call-failed", "Task", map[string]any{
			"description": "Failed launch", "subagent_type": "Plan", "prompt": "try it",
		}),
		claudeToolResult("root-id", "call-failed", true),
	})

	node := findClaudeAgent(t, fixture.build(t), "Failed launch")
	assertClaudeAgent(t, node, model.AgentNode{
		ID:                 claudeAgentID(fixture.root, "launch:"+claudeMainID(fixture.root)+":call-failed"),
		ParentID:           claudeMainID(fixture.root),
		Depth:              1,
		Kind:               model.AgentKindSubagent,
		Label:              "Failed launch",
		Role:               "Plan",
		InstructionPreview: "try it",
		LaunchSeq:          intPointer(0),
		LaunchCallID:       "call-failed",
		Status:             model.AgentStatusFailed,
		TraceAvailability:  model.TraceAvailabilityUnavailable,
		LinkQuality:        model.AgentLinkQualityUnavailable,
		LinkMethod:         model.AgentLinkMethodUnavailable,
	})
}

func TestClaudeAgentGraphSuccessfulResultWithoutArtifactStaysUnknown(t *testing.T) {
	fixture := newClaudeAgentFixture(t, []any{
		claudeToolUse("root-id", "call-unknown", "Agent", map[string]any{
			"description": "Unknown launch", "subagent_type": "Explore", "prompt": "try it",
		}),
		claudeToolResult("root-id", "call-unknown", false),
	})

	node := findClaudeAgent(t, fixture.build(t), "Unknown launch")
	if node.Status != model.AgentStatusUnknown || node.TraceAvailability != model.TraceAvailabilityUnavailable ||
		node.LinkQuality != model.AgentLinkQualityUnavailable || node.LinkMethod != model.AgentLinkMethodUnavailable {
		t.Fatalf("successful result node = %#v", node)
	}
}

func TestClaudeAgentGraphDuplicateCallIDProducesOneNode(t *testing.T) {
	fixture := newClaudeAgentFixture(t, []any{
		claudeToolUse("root-id", "call-shared", "Agent", map[string]any{
			"description": "Shared", "subagent_type": "Explore", "prompt": "inspect",
		}),
	})
	fixture.addChild(t, "agent-shared", []any{
		claudeSidechainUser("root-id", "shared-id", "child prompt"),
		claudeToolUseForAgent("root-id", "shared-id", "call-shared", "Agent", map[string]any{
			"description": "Duplicate transcript entry", "prompt": "inspect",
		}),
	}, claudeChildMeta{Description: "Shared", ToolUseID: "call-shared", SpawnDepth: 1})

	graph := fixture.build(t)
	if len(graph.Agents) != 2 {
		t.Fatalf("agents = %#v, want Main plus one shared-call node", graph.Agents)
	}
	if graph.Agents[1].Label != "Shared" || graph.Agents[1].TraceAvailability != model.TraceAvailabilityAvailable {
		t.Fatalf("shared node = %#v", graph.Agents[1])
	}
}

func TestClaudeAgentGraphUsesStablePreorder(t *testing.T) {
	fixture := newClaudeAgentFixture(t, []any{
		claudeToolUse("root-id", "call-a", "Agent", map[string]any{"description": "A", "prompt": "A"}),
		claudeToolUse("root-id", "call-b", "Agent", map[string]any{"description": "B", "prompt": "B"}),
	})
	fixture.addChild(t, "agent-a", []any{
		claudeSidechainUser("root-id", "a-id", "A child"),
		claudeToolUseForAgent("root-id", "a-id", "call-a1", "Agent", map[string]any{
			"description": "A1", "prompt": "A1",
		}),
	}, claudeChildMeta{Description: "A", ToolUseID: "call-a", SpawnDepth: 1})
	fixture.addChild(t, "agent-b", []any{
		claudeSidechainUser("root-id", "b-id", "B child"),
	}, claudeChildMeta{Description: "B", ToolUseID: "call-b", SpawnDepth: 1})
	fixture.addChild(t, "agent-a1", []any{
		claudeSidechainUser("root-id", "a1-id", "A1 child"),
	}, claudeChildMeta{Description: "A1", ToolUseID: "call-a1", SpawnDepth: 2})

	graph := fixture.build(t)
	labels := make([]string, len(graph.Agents))
	for i, node := range graph.Agents {
		labels[i] = node.Label
	}
	want := []string{"Main", "A", "A1", "B"}
	if len(labels) != len(want) {
		t.Fatalf("agent order = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("agent order = %v, want %v", labels, want)
		}
	}
}

func TestClaudeAgentGraphInputsIncludeChildTracesAndSidecars(t *testing.T) {
	fixture := newClaudeAgentFixture(t, nil)
	child := fixture.addChild(t, "agent-child", []any{
		claudeSidechainUser("root-id", "child-id", "child prompt"),
	}, claudeChildMeta{Description: "Child", ToolUseID: "call-child", SpawnDepth: 1})
	fixture.addMetaOnly(t, "agent-missing", claudeChildMeta{Description: "Missing", ToolUseID: "call-missing"})

	inputs, err := fixture.adapter.AgentGraphInputs(fixture.root, fixture.catalog)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		fixture.root.Path: true,
		child.Path:        true,
		filepath.Join(fixture.subagentsDir, "agent-child.meta.json"):   true,
		filepath.Join(fixture.subagentsDir, "agent-missing.meta.json"): true,
	}
	if len(inputs) != len(want) {
		t.Fatalf("graph inputs = %v, want %v", inputs, want)
	}
	for _, path := range inputs {
		if !want[path] {
			t.Fatalf("unexpected graph input %q in %v", path, inputs)
		}
	}
}

type claudeAgentFixture struct {
	adapter      Adapter
	root         model.SessionMeta
	catalog      []model.SessionMeta
	subagentsDir string
}

type claudeChildMeta struct {
	AgentType   string `json:"agentType,omitempty"`
	Description string `json:"description,omitempty"`
	ToolUseID   string `json:"toolUseId,omitempty"`
	SpawnDepth  int    `json:"spawnDepth,omitempty"`
}

func newClaudeAgentFixture(t *testing.T, rootLines []any) *claudeAgentFixture {
	t.Helper()
	dir := t.TempDir()
	a := Adapter{Dir: dir}
	rootPath := filepath.Join(dir, "root-id.jsonl")
	lines := append([]any{claudeUser("root-id", "root prompt")}, rootLines...)
	writeClaudeAgentJSONL(t, rootPath, lines...)
	root, err := a.Summarize(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	subagentsDir := filepath.Join(dir, "root-id", "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &claudeAgentFixture{adapter: a, root: root, subagentsDir: subagentsDir}
}

func (f *claudeAgentFixture) addChild(t *testing.T, basename string, lines []any, meta claudeChildMeta) model.SessionMeta {
	t.Helper()
	path := filepath.Join(f.subagentsDir, basename+".jsonl")
	writeClaudeAgentJSONL(t, path, lines...)
	f.writeMeta(t, basename, meta)
	session, err := f.adapter.Summarize(path)
	if err != nil {
		t.Fatal(err)
	}
	f.catalog = append(f.catalog, session)
	return session
}

func (f *claudeAgentFixture) addMetaOnly(t *testing.T, basename string, meta claudeChildMeta) {
	t.Helper()
	f.writeMeta(t, basename, meta)
}

func (f *claudeAgentFixture) writeMeta(t *testing.T, basename string, meta claudeChildMeta) {
	t.Helper()
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.subagentsDir, basename+".meta.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *claudeAgentFixture) build(t *testing.T) *model.AgentGraph {
	t.Helper()
	graph, err := f.adapter.BuildAgentGraph(f.root, f.catalog)
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

func claudeUser(sessionID, text string) map[string]any {
	return map[string]any{
		"type": "user", "timestamp": "2026-07-15T00:00:00Z", "sessionId": sessionID,
		"message": map[string]any{"role": "user", "content": text},
	}
}

func claudeSidechainUser(sessionID, agentID, text string) map[string]any {
	line := claudeUser(sessionID, text)
	line["agentId"] = agentID
	line["isSidechain"] = true
	return line
}

func claudeToolUse(sessionID, id, name string, input map[string]any) map[string]any {
	return claudeMessageItem(sessionID, "", "assistant", map[string]any{
		"type": "tool_use", "id": id, "name": name, "input": input,
	})
}

func claudeToolUseForAgent(sessionID, agentID, id, name string, input map[string]any) map[string]any {
	return claudeMessageItem(sessionID, agentID, "assistant", map[string]any{
		"type": "tool_use", "id": id, "name": name, "input": input,
	})
}

func claudeToolResult(sessionID, toolUseID string, isError bool) map[string]any {
	return claudeMessageItem(sessionID, "", "user", map[string]any{
		"type": "tool_result", "tool_use_id": toolUseID, "content": "result", "is_error": isError,
	})
}

func claudeToolResultForAgent(sessionID, agentID, toolUseID string, isError bool) map[string]any {
	return claudeMessageItem(sessionID, agentID, "user", map[string]any{
		"type": "tool_result", "tool_use_id": toolUseID, "content": "result", "is_error": isError,
	})
}

func claudeMessageItem(sessionID, agentID, role string, item map[string]any) map[string]any {
	line := map[string]any{
		"type": role, "timestamp": "2026-07-15T00:00:01Z", "sessionId": sessionID,
		"message": map[string]any{"role": role, "content": []any{item}},
	}
	if agentID != "" {
		line["agentId"] = agentID
		line["isSidechain"] = true
	}
	return line
}

func writeClaudeAgentJSONL(t *testing.T, path string, values ...any) {
	t.Helper()
	var content []byte
	for _, value := range values {
		line, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		content = append(content, line...)
		content = append(content, '\n')
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func findClaudeAgent(t *testing.T, graph *model.AgentGraph, label string) model.AgentNode {
	t.Helper()
	for _, node := range graph.Agents {
		if node.Label == label {
			return node
		}
	}
	t.Fatalf("agent %q not found in %#v", label, graph.Agents)
	return model.AgentNode{}
}

func assertClaudeAgent(t *testing.T, got, want model.AgentNode) {
	t.Helper()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("agent:\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}

func claudeMainNode(root model.SessionMeta) model.AgentNode {
	return model.AgentNode{
		ID:                claudeMainID(root),
		Depth:             0,
		Kind:              model.AgentKindMain,
		Label:             "Main",
		Status:            model.AgentStatusMain,
		TraceAvailability: model.TraceAvailabilityAvailable,
		TraceSessionKey:   root.Key,
		TraceEventCount:   root.EventCount,
		LinkQuality:       model.AgentLinkQualityExact,
		LinkMethod:        model.AgentLinkMethodRoot,
	}
}

func claudeMainID(root model.SessionMeta) string {
	return claudeAgentID(root, "root:"+root.Key)
}

func claudeAgentID(root model.SessionMeta, identity string) string {
	return baseadapter.AgentNodeID((Adapter{}).Harness(), root.Key, identity)
}

func intPointer(value int) *int {
	return &value
}
