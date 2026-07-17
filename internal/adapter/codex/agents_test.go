package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	baseadapter "github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/model"
)

type codexAgentChildFixture struct {
	id        string
	parentID  string
	depth     int
	agentPath string
	label     string
	role      string
	lines     []any
}

type codexAgentFixture struct {
	adapter Adapter
	root    model.SessionMeta
	catalog []model.SessionMeta
}

type rawAgentJSONL string

func TestCodexAgentGraphFixtures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (codexAgentFixture, []model.AgentNode)
	}{
		{
			name: "exact",
			setup: func(t *testing.T) (codexAgentFixture, []model.AgentNode) {
				fixture := newCodexAgentFixture(t, []any{
					call("", "fc-read", "call-read", "exec_command", map[string]any{"cmd": "true"}),
					output("", "call-read", "Process exited with code 0"),
					call("", "fc-exact", "call-exact", "spawn_agent", map[string]any{
						"agent_type": "reviewer",
						"message":    "  review\n  this change  ",
					}),
					output("", "call-exact", `{"agent_id":"agent-exact","nickname":"Launch Nick"}`),
				}, codexAgentChildFixture{
					id:       "agent-exact",
					parentID: "root-session",
					depth:    1,
					label:    "Tesla",
					role:     "reviewer",
					lines: []any{
						call("", "fc-child", "call-child", "exec_command", map[string]any{"cmd": "true"}),
						output("", "call-child", "Process exited with code 0"),
					},
				})
				seq := 1
				return fixture, []model.AgentNode{
					mainAgentNode(fixture.root),
					{
						ID:                 codexAgentID(fixture.root, "codex-agent:agent-exact"),
						ParentID:           codexMainID(fixture.root),
						Depth:              1,
						Kind:               model.AgentKindSubagent,
						Label:              "Tesla",
						Role:               "reviewer",
						InstructionPreview: "review this change",
						LaunchSeq:          &seq,
						LaunchCallID:       "call-exact",
						Status:             model.AgentStatusLaunched,
						TraceAvailability:  model.TraceAvailabilityAvailable,
						TraceSessionKey:    fixture.catalog[0].Key,
						TraceEventCount:    1,
						LinkQuality:        model.AgentLinkQualityExact,
						LinkMethod:         model.AgentLinkMethodCodexAgentID,
					},
				}
			},
		},
		{
			name: "failed",
			setup: func(t *testing.T) (codexAgentFixture, []model.AgentNode) {
				fixture := newCodexAgentFixture(t, []any{
					call("", "fc-failed", "call-failed", "spawn_agent", map[string]any{
						"agent_type": "default",
						"message":    "try the fork",
					}),
					output("", "call-failed", "fork requires a previous turn"),
				})
				seq := 0
				return fixture, []model.AgentNode{
					mainAgentNode(fixture.root),
					{
						ID:                 codexAgentID(fixture.root, "launch:"+codexMainID(fixture.root)+":call-failed"),
						ParentID:           codexMainID(fixture.root),
						Depth:              1,
						Kind:               model.AgentKindSubagent,
						Label:              "Subagent",
						Role:               "default",
						InstructionPreview: "try the fork",
						LaunchSeq:          &seq,
						LaunchCallID:       "call-failed",
						Status:             model.AgentStatusFailed,
						TraceAvailability:  model.TraceAvailabilityUnavailable,
						LinkQuality:        model.AgentLinkQualityUnavailable,
						LinkMethod:         model.AgentLinkMethodUnavailable,
					},
				}
			},
		},
		{
			name: "missing",
			setup: func(t *testing.T) (codexAgentFixture, []model.AgentNode) {
				fixture := newCodexAgentFixture(t, []any{
					call("", "fc-missing", "call-missing", "spawn_agent", map[string]any{
						"agent_type": "explorer",
						"message":    "find the path",
					}),
					output("", "call-missing", `{"agent_id":"agent-missing","nickname":"Nova"}`),
				})
				seq := 0
				return fixture, []model.AgentNode{
					mainAgentNode(fixture.root),
					{
						ID:                 codexAgentID(fixture.root, "codex-agent:agent-missing"),
						ParentID:           codexMainID(fixture.root),
						Depth:              1,
						Kind:               model.AgentKindSubagent,
						Label:              "Nova",
						Role:               "explorer",
						InstructionPreview: "find the path",
						LaunchSeq:          &seq,
						LaunchCallID:       "call-missing",
						Status:             model.AgentStatusLaunched,
						TraceAvailability:  model.TraceAvailabilityMissing,
						LinkQuality:        model.AgentLinkQualityExact,
						LinkMethod:         model.AgentLinkMethodCodexAgentID,
					},
				}
			},
		},
		{
			name: "nested",
			setup: func(t *testing.T) (codexAgentFixture, []model.AgentNode) {
				fixture := newCodexAgentFixture(t, []any{
					call("", "fc-child", "call-child", "spawn_agent", map[string]any{"message": "first"}),
					output("", "call-child", `{"agent_id":"agent-child"}`),
				},
					codexAgentChildFixture{
						id:       "agent-child",
						parentID: "root-session",
						depth:    1,
						label:    "Child",
						lines: []any{
							call("", "fc-read", "call-read", "exec_command", map[string]any{"cmd": "true"}),
							output("", "call-read", "Process exited with code 0"),
							call("", "fc-grand", "call-grand", "spawn_agent", map[string]any{"message": "second"}),
							output("", "call-grand", `{"agent_id":"agent-grand"}`),
						},
					},
					codexAgentChildFixture{
						id:       "agent-grand",
						parentID: "agent-child",
						depth:    2,
						label:    "Grandchild",
					},
				)
				rootSeq, nestedSeq := 0, 1
				childID := codexAgentID(fixture.root, "codex-agent:agent-child")
				return fixture, []model.AgentNode{
					mainAgentNode(fixture.root),
					{
						ID:                 childID,
						ParentID:           codexMainID(fixture.root),
						Depth:              1,
						Kind:               model.AgentKindSubagent,
						Label:              "Child",
						InstructionPreview: "first",
						LaunchSeq:          &rootSeq,
						LaunchCallID:       "call-child",
						Status:             model.AgentStatusLaunched,
						TraceAvailability:  model.TraceAvailabilityAvailable,
						TraceSessionKey:    fixture.catalog[0].Key,
						TraceEventCount:    2,
						LinkQuality:        model.AgentLinkQualityExact,
						LinkMethod:         model.AgentLinkMethodCodexAgentID,
					},
					{
						ID:                 codexAgentID(fixture.root, "codex-agent:agent-grand"),
						ParentID:           childID,
						Depth:              2,
						Kind:               model.AgentKindSubagent,
						Label:              "Grandchild",
						InstructionPreview: "second",
						LaunchSeq:          &nestedSeq,
						LaunchCallID:       "call-grand",
						Status:             model.AgentStatusLaunched,
						TraceAvailability:  model.TraceAvailabilityAvailable,
						TraceSessionKey:    fixture.catalog[1].Key,
						TraceEventCount:    0,
						LinkQuality:        model.AgentLinkQualityExact,
						LinkMethod:         model.AgentLinkMethodCodexAgentID,
					},
				}
			},
		},
		{
			name: "orphan",
			setup: func(t *testing.T) (codexAgentFixture, []model.AgentNode) {
				fixture := newCodexAgentFixture(t, nil, codexAgentChildFixture{
					id:       "agent-orphan",
					parentID: "root-session",
					depth:    1,
					label:    "Orphan",
					role:     "explorer",
				})
				return fixture, []model.AgentNode{
					mainAgentNode(fixture.root),
					{
						ID:                codexAgentID(fixture.root, "session:"+fixture.catalog[0].Key),
						ParentID:          codexMainID(fixture.root),
						Depth:             1,
						Kind:              model.AgentKindSubagent,
						Label:             "Orphan",
						Role:              "explorer",
						Status:            model.AgentStatusUnknown,
						TraceAvailability: model.TraceAvailabilityAvailable,
						TraceSessionKey:   fixture.catalog[0].Key,
						TraceEventCount:   0,
						LinkQuality:       model.AgentLinkQualityDerived,
						LinkMethod:        model.AgentLinkMethodCodexParentThreadID,
					},
				}
			},
		},
		{
			name: "malformed",
			setup: func(t *testing.T) (codexAgentFixture, []model.AgentNode) {
				fixture := newCodexAgentFixture(t, []any{
					rawAgentJSONL(`{"type":"response_item","payload":`),
					call("", "fc-valid", "call-valid", "spawn_agent", map[string]any{"message": "valid"}),
					output("", "call-valid", `{"agent_id":"agent-valid"}`),
				}, codexAgentChildFixture{
					id:       "agent-valid",
					parentID: "root-session",
					depth:    1,
					label:    "Valid",
				})
				seq := 0
				return fixture, []model.AgentNode{
					mainAgentNode(fixture.root),
					{
						ID:                 codexAgentID(fixture.root, "codex-agent:agent-valid"),
						ParentID:           codexMainID(fixture.root),
						Depth:              1,
						Kind:               model.AgentKindSubagent,
						Label:              "Valid",
						InstructionPreview: "valid",
						LaunchSeq:          &seq,
						LaunchCallID:       "call-valid",
						Status:             model.AgentStatusLaunched,
						TraceAvailability:  model.TraceAvailabilityAvailable,
						TraceSessionKey:    fixture.catalog[0].Key,
						TraceEventCount:    0,
						LinkQuality:        model.AgentLinkQualityExact,
						LinkMethod:         model.AgentLinkMethodCodexAgentID,
					},
				}
			},
		},
		{
			name: "zero event",
			setup: func(t *testing.T) (codexAgentFixture, []model.AgentNode) {
				fixture := newCodexAgentFixture(t, []any{
					call("", "fc-zero", "call-zero", "spawn_agent", map[string]any{"message": "zero"}),
					output("", "call-zero", `{"agent_id":"agent-zero"}`),
				}, codexAgentChildFixture{
					id:       "agent-zero",
					parentID: "root-session",
					depth:    1,
					label:    "Zero",
				})
				seq := 0
				return fixture, []model.AgentNode{
					mainAgentNode(fixture.root),
					{
						ID:                 codexAgentID(fixture.root, "codex-agent:agent-zero"),
						ParentID:           codexMainID(fixture.root),
						Depth:              1,
						Kind:               model.AgentKindSubagent,
						Label:              "Zero",
						InstructionPreview: "zero",
						LaunchSeq:          &seq,
						LaunchCallID:       "call-zero",
						Status:             model.AgentStatusLaunched,
						TraceAvailability:  model.TraceAvailabilityAvailable,
						TraceSessionKey:    fixture.catalog[0].Key,
						TraceEventCount:    0,
						LinkQuality:        model.AgentLinkQualityExact,
						LinkMethod:         model.AgentLinkMethodCodexAgentID,
					},
				}
			},
		},
		{
			name: "no result is unknown",
			setup: func(t *testing.T) (codexAgentFixture, []model.AgentNode) {
				fixture := newCodexAgentFixture(t, []any{
					call("", "fc-unknown", "call-unknown", "spawn_agent", map[string]any{
						"agent_type": "default",
						"message":    "wait for output",
					}),
				})
				seq := 0
				return fixture, []model.AgentNode{
					mainAgentNode(fixture.root),
					{
						ID:                 codexAgentID(fixture.root, "launch:"+codexMainID(fixture.root)+":call-unknown"),
						ParentID:           codexMainID(fixture.root),
						Depth:              1,
						Kind:               model.AgentKindSubagent,
						Label:              "Subagent",
						Role:               "default",
						InstructionPreview: "wait for output",
						LaunchSeq:          &seq,
						LaunchCallID:       "call-unknown",
						Status:             model.AgentStatusUnknown,
						TraceAvailability:  model.TraceAvailabilityUnavailable,
						LinkQuality:        model.AgentLinkQualityUnavailable,
						LinkMethod:         model.AgentLinkMethodUnavailable,
					},
				}
			},
		},
		{
			name: "stable ids and order",
			setup: func(t *testing.T) (codexAgentFixture, []model.AgentNode) {
				fixture := newCodexAgentFixture(t, []any{
					call("", "fc-zulu", "call-zulu", "spawn_agent", map[string]any{"message": "zulu"}),
					output("", "call-zulu", `{"agent_id":"agent-zulu"}`),
					call("", "fc-read", "call-read", "exec_command", map[string]any{"cmd": "true"}),
					output("", "call-read", "Process exited with code 0"),
					call("", "fc-alpha", "call-alpha", "spawn_agent", map[string]any{"message": "alpha"}),
					output("", "call-alpha", `{"agent_id":"agent-alpha"}`),
				},
					codexAgentChildFixture{id: "agent-orphan", parentID: "root-session", depth: 1, label: "Aaron"},
					codexAgentChildFixture{id: "agent-alpha", parentID: "root-session", depth: 1, label: "Alpha"},
					codexAgentChildFixture{id: "agent-zulu", parentID: "root-session", depth: 1, label: "Zulu"},
				)
				zuluSeq, alphaSeq := 0, 2
				return fixture, []model.AgentNode{
					mainAgentNode(fixture.root),
					availableCodexLaunch(fixture.root, fixture.catalog[2], "agent-zulu", "call-zulu", "zulu", zuluSeq),
					availableCodexLaunch(fixture.root, fixture.catalog[1], "agent-alpha", "call-alpha", "alpha", alphaSeq),
					{
						ID:                codexAgentID(fixture.root, "session:"+fixture.catalog[0].Key),
						ParentID:          codexMainID(fixture.root),
						Depth:             1,
						Kind:              model.AgentKindSubagent,
						Label:             "Aaron",
						Status:            model.AgentStatusUnknown,
						TraceAvailability: model.TraceAvailabilityAvailable,
						TraceSessionKey:   fixture.catalog[0].Key,
						LinkQuality:       model.AgentLinkQualityDerived,
						LinkMethod:        model.AgentLinkMethodCodexParentThreadID,
					},
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, want := test.setup(t)
			first, err := fixture.adapter.BuildAgentGraph(fixture.root, fixture.catalog)
			if err != nil {
				t.Fatal(err)
			}
			second, err := fixture.adapter.BuildAgentGraph(fixture.root, fixture.catalog)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("successive builds differ:\nfirst:  %#v\nsecond: %#v", first, second)
			}
			if first.Version != model.AgentGraphVersion || first.RootSessionKey != fixture.root.Key {
				t.Fatalf("graph header = %#v", first)
			}
			if !reflect.DeepEqual(first.Agents, want) {
				t.Fatalf("agents:\n got: %#v\nwant: %#v", first.Agents, want)
			}
		})
	}
}

func TestCodexAgentGraphLegacyTaskNameMergesDerivedChild(t *testing.T) {
	fixture := newCodexAgentFixture(t, []any{
		call("", "fc-legacy", "call-legacy", "spawn_agent", map[string]any{
			"message": "review the legacy trace",
		}),
		output("", "call-legacy", `{"task_name":"/root/legacy-review"}`),
	}, codexAgentChildFixture{
		id:        "legacy-child",
		parentID:  "root-session",
		depth:     1,
		agentPath: "/root/legacy-review",
		label:     "Legacy Reviewer",
	})

	graph, err := fixture.adapter.BuildAgentGraph(fixture.root, fixture.catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Agents) != 2 {
		t.Fatalf("agents = %#v, want Main plus one merged legacy child", graph.Agents)
	}
	node := graph.Agents[1]
	if node.Label != "Legacy Reviewer" || node.LaunchCallID != "call-legacy" ||
		node.Status != model.AgentStatusLaunched || node.TraceAvailability != model.TraceAvailabilityAvailable ||
		node.TraceSessionKey != fixture.catalog[0].Key {
		t.Fatalf("legacy node = %#v", node)
	}
}

func TestCodexAgentGraphUnknownJSONObjectIsNotFailure(t *testing.T) {
	fixture := newCodexAgentFixture(t, []any{
		call("", "fc-unknown-shape", "call-unknown-shape", "spawn_agent", map[string]any{"message": "try it"}),
		output("", "call-unknown-shape", `{"message":"accepted"}`),
	})

	graph, err := fixture.adapter.BuildAgentGraph(fixture.root, fixture.catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Agents) != 2 || graph.Agents[1].Status != model.AgentStatusUnknown {
		t.Fatalf("unknown output node = %#v", graph.Agents)
	}
}

func TestCodexAgentGraphUsesStablePreorder(t *testing.T) {
	fixture := newCodexAgentFixture(t, []any{
		call("", "fc-a", "call-a", "spawn_agent", map[string]any{"message": "A"}),
		output("", "call-a", `{"agent_id":"agent-a"}`),
		call("", "fc-b", "call-b", "spawn_agent", map[string]any{"message": "B"}),
		output("", "call-b", `{"agent_id":"agent-b"}`),
	},
		codexAgentChildFixture{
			id: "agent-a", parentID: "root-session", depth: 1, label: "A",
			lines: []any{
				call("", "fc-a1", "call-a1", "spawn_agent", map[string]any{"message": "A1"}),
				output("", "call-a1", `{"agent_id":"agent-a1"}`),
			},
		},
		codexAgentChildFixture{id: "agent-b", parentID: "root-session", depth: 1, label: "B"},
		codexAgentChildFixture{id: "agent-a1", parentID: "agent-a", depth: 2, label: "A1"},
	)

	graph, err := fixture.adapter.BuildAgentGraph(fixture.root, fixture.catalog)
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]string, len(graph.Agents))
	for i, node := range graph.Agents {
		labels[i] = node.Label
	}
	want := []string{"Main", "A", "A1", "B"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("agent order = %v, want %v", labels, want)
	}
}

func TestCodexAgentGraphInputsIncludeNestedChildren(t *testing.T) {
	fixture := newCodexAgentFixture(t, nil,
		codexAgentChildFixture{id: "agent-a", parentID: "root-session", depth: 1, label: "A"},
		codexAgentChildFixture{id: "agent-a1", parentID: "agent-a", depth: 2, label: "A1"},
		codexAgentChildFixture{id: "unrelated", parentID: "other-root", depth: 1, label: "Other"},
	)

	inputs, err := fixture.adapter.AgentGraphInputs(fixture.root, fixture.catalog)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{fixture.root.Path, fixture.catalog[0].Path, fixture.catalog[1].Path}
	sort.Strings(want)
	if !reflect.DeepEqual(inputs, want) {
		t.Fatalf("graph inputs = %v, want %v", inputs, want)
	}
}

func newCodexAgentFixture(t *testing.T, rootLines []any, children ...codexAgentChildFixture) codexAgentFixture {
	t.Helper()
	dir := t.TempDir()
	a := Adapter{Dir: dir}
	rootPath := filepath.Join(dir, "root.jsonl")
	writeAgentJSONL(t, rootPath, append([]any{codexRootSessionMeta()}, rootLines...)...)
	root, err := a.Summarize(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog := make([]model.SessionMeta, 0, len(children))
	for _, child := range children {
		path := filepath.Join(dir, child.id+".jsonl")
		lines := append([]any{codexChildSessionMeta(child)}, child.lines...)
		writeAgentJSONL(t, path, lines...)
		meta, err := a.Summarize(path)
		if err != nil {
			t.Fatal(err)
		}
		catalog = append(catalog, meta)
	}
	return codexAgentFixture{adapter: a, root: root, catalog: catalog}
}

func codexRootSessionMeta() map[string]any {
	return map[string]any{
		"type": "session_meta",
		"payload": map[string]any{
			"id": "root-session",
		},
	}
}

func codexChildSessionMeta(child codexAgentChildFixture) map[string]any {
	return map[string]any{
		"type": "session_meta",
		"payload": map[string]any{
			"id":               child.id,
			"session_id":       "root-session",
			"parent_thread_id": child.parentID,
			"thread_source":    "subagent",
			"source": map[string]any{
				"subagent": map[string]any{
					"thread_spawn": map[string]any{
						"parent_thread_id": child.parentID,
						"depth":            child.depth,
						"agent_path":       child.agentPath,
						"agent_nickname":   child.label,
						"agent_role":       child.role,
					},
				},
			},
		},
	}
}

func writeAgentJSONL(t *testing.T, path string, values ...any) {
	t.Helper()
	var content []byte
	for _, value := range values {
		if raw, ok := value.(rawAgentJSONL); ok {
			content = append(content, raw...)
			content = append(content, '\n')
			continue
		}
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

func mainAgentNode(root model.SessionMeta) model.AgentNode {
	return model.AgentNode{
		ID:                codexMainID(root),
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

func availableCodexLaunch(root model.SessionMeta, child model.SessionMeta, agentID, callID, message string, seq int) model.AgentNode {
	return model.AgentNode{
		ID:                 codexAgentID(root, "codex-agent:"+agentID),
		ParentID:           codexMainID(root),
		Depth:              child.Agent.Depth,
		Kind:               model.AgentKindSubagent,
		Label:              child.Agent.Label,
		InstructionPreview: message,
		LaunchSeq:          &seq,
		LaunchCallID:       callID,
		Status:             model.AgentStatusLaunched,
		TraceAvailability:  model.TraceAvailabilityAvailable,
		TraceSessionKey:    child.Key,
		TraceEventCount:    child.EventCount,
		LinkQuality:        model.AgentLinkQualityExact,
		LinkMethod:         model.AgentLinkMethodCodexAgentID,
	}
}

func codexMainID(root model.SessionMeta) string {
	return codexAgentID(root, "root:"+root.Key)
}

func codexAgentID(root model.SessionMeta, identity string) string {
	return baseadapter.AgentNodeID((Adapter{}).Harness(), root.Key, identity)
}
