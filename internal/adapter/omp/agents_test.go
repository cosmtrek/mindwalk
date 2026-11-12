package omp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/model"
)

func taskResult(id, parent, callID string, isError bool, progress []map[string]any) string {
	msg := map[string]any{
		"role":       "toolResult",
		"toolCallId": callID,
		"toolName":   "task",
		"content":    []any{map[string]any{"type": "text", "text": "started"}},
		"isError":    isError,
	}
	if progress != nil {
		msg["details"] = map[string]any{"async": map[string]any{"state": "running"}, "progress": progress, "results": []any{}}
	}
	return message(id, parent, msg)
}

func TestBuildAgentGraphLinksTaskLaunchesToSessionFiles(t *testing.T) {
	repo := t.TempDir()
	store := t.TempDir()
	rootPath := filepath.Join(store, "-repo", "2026-11-01T10-00-00-000Z_root.jsonl")
	artifacts := strings.TrimSuffix(rootPath, ".jsonl")
	writeLines(t, rootPath,
		titleSlot("Root"),
		header("root", repo, ""),
		userMessage("u1", "", "user", "audit the services"),
		toolCall("a1", "u1", "c-read", "read", map[string]any{"path": "README.md"}),
		toolResult("r1", "a1", "c-read", "readme", false),
		toolCall("a2", "r1", "c-task", "task", map[string]any{"tasks": []any{
			map[string]any{"name": "Audit", "task": "audit"},
			map[string]any{"name": "Lost", "task": "never wrote a session"},
		}}),
		taskResult("r2", "a2", "c-task", false, []map[string]any{
			{"id": "Audit", "agent": "task", "status": "pending", "assignment": "# Target\naudit service A"},
			{"id": "Lost", "agent": "scout", "status": "pending", "assignment": "look around"},
		}),
		toolCall("a3", "r2", "c-rejected", "task", map[string]any{"tasks": []any{map[string]any{"name": "Rejected", "task": "x"}}}),
		taskResult("r3", "a3", "c-rejected", true, nil),
	)
	auditPath := writeLines(t, filepath.Join(artifacts, "Audit.jsonl"),
		titleSlot(""),
		header("audit", repo, rootPath),
		entry("session_init", "i1", "", map[string]any{"agent": "task"}),
		userMessage("u1", "i1", "agent", "audit service A"),
		toolCall("a1", "u1", "c-nested", "task", map[string]any{"tasks": []any{map[string]any{"name": "Probe", "agent": "scout", "task": "probe"}}}),
		taskResult("r1", "a1", "c-nested", false, []map[string]any{{"id": "Audit.Probe", "agent": "scout", "status": "pending", "assignment": "probe A"}}),
	)
	probePath := writeLines(t, filepath.Join(artifacts, "Audit", "Audit.Probe.jsonl"),
		titleSlot(""),
		header("probe", repo, auditPath),
		entry("session_init", "i1", "", map[string]any{"agent": "scout"}),
	)
	// a subagent session whose launch the parent log does not record
	orphanPath := writeLines(t, filepath.Join(artifacts, "Orphan.jsonl"),
		titleSlot(""),
		header("orphan", repo, rootPath),
	)
	// other .jsonl artifacts in the directory are not agents
	writeLines(t, filepath.Join(artifacts, "notes.jsonl"), `{"note":1}`)

	a := Adapter{Dir: store}
	var catalog []model.SessionMeta
	for _, path := range []string{rootPath, auditPath, probePath, orphanPath} {
		meta, err := a.Summarize(path)
		if err != nil {
			t.Fatal(err)
		}
		catalog = append(catalog, meta)
	}
	root := catalog[0]

	graph, err := a.BuildAgentGraph(root, catalog)
	if err != nil {
		t.Fatal(err)
	}
	byLabel := map[string]model.AgentNode{}
	var order []string
	for _, node := range graph.Agents {
		byLabel[node.Label] = node
		order = append(order, node.Label)
	}
	// preorder: launched children by seq, then nested, then unlaunched
	if want := "Main,Audit,Probe,Lost,Rejected,Orphan"; strings.Join(order, ",") != want {
		t.Fatalf("order = %v, want %s", order, want)
	}

	main := byLabel["Main"]
	audit := byLabel["Audit"]
	if audit.ParentID != main.ID || audit.Depth != 1 || audit.LinkMethod != model.AgentLinkMethodOmpTaskID ||
		audit.LinkQuality != model.AgentLinkQualityExact || audit.LaunchCallID != "c-task" ||
		audit.LaunchSeq == nil || *audit.LaunchSeq != 1 || audit.Role != "task" ||
		audit.InstructionPreview != "# Target audit service A" ||
		audit.TraceAvailability != model.TraceAvailabilityAvailable || audit.TraceSessionKey != catalog[1].Key ||
		audit.TraceEventCount != 1 {
		t.Fatalf("audit = %#v", audit)
	}
	rootTrace, err := a.Parse(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if rootTrace.Events[*audit.LaunchSeq].Tool != "task" {
		t.Fatalf("launch seq %d is %#v in the parent trace", *audit.LaunchSeq, rootTrace.Events[*audit.LaunchSeq])
	}

	probe := byLabel["Probe"]
	if probe.ParentID != audit.ID || probe.Depth != 2 || probe.LaunchCallID != "c-nested" || probe.Role != "scout" ||
		probe.TraceSessionKey != catalog[2].Key {
		t.Fatalf("probe = %#v", probe)
	}
	if lost := byLabel["Lost"]; lost.ParentID != main.ID || lost.Status != model.AgentStatusUnknown ||
		lost.TraceAvailability != model.TraceAvailabilityUnavailable || lost.LaunchCallID != "c-task" {
		t.Fatalf("lost = %#v", lost)
	}
	if rejected := byLabel["Rejected"]; rejected.Status != model.AgentStatusFailed || rejected.LaunchSeq == nil || *rejected.LaunchSeq != 2 {
		t.Fatalf("rejected = %#v", rejected)
	}
	if orphan := byLabel["Orphan"]; orphan.LinkMethod != model.AgentLinkMethodOmpSessionDirectory ||
		orphan.LinkQuality != model.AgentLinkQualityDerived || orphan.LaunchSeq != nil ||
		orphan.TraceSessionKey != catalog[3].Key {
		t.Fatalf("orphan = %#v", orphan)
	}

	inputs, err := a.AgentGraphInputs(root, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 5 {
		t.Fatalf("inputs = %v", inputs)
	}
}
