package codex

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/judge"
	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestParseCodexSession(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	session := filepath.Join(dir, "rollout-2026-07-09T00-00-00-codex-demo.jsonl")
	index := filepath.Join(dir, "session_index.jsonl")
	writeJSONL(t, index, map[string]any{
		"id":          "codex-demo",
		"thread_name": "Codex demo trace",
		"updated_at":  "2026-07-09T00:00:09Z",
	})
	writeJSONL(t, session,
		map[string]any{
			"timestamp": "2026-07-09T00:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":         "codex-demo",
				"session_id": "codex-demo",
				"timestamp":  "2026-07-09T00:00:00Z",
				"cwd":        filepath.ToSlash(root),
				"git": map[string]any{
					"branch":      "main",
					"commit_hash": "abc123",
				},
			},
		},
		map[string]any{
			"timestamp": "2026-07-09T00:00:01Z",
			"type":      "turn_context",
			"payload": map[string]any{
				"cwd":   filepath.ToSlash(root),
				"model": "gpt-5.5",
			},
		},
		map[string]any{
			"timestamp": "2026-07-09T00:00:02Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": "inspect"}},
			},
		},
		call("2026-07-09T00:00:03Z", "fc-read", "call-read", "exec_command", map[string]any{
			"cmd":     "sed -n '1,40p' README.md",
			"workdir": filepath.ToSlash(root),
		}),
		output("2026-07-09T00:00:04Z", "call-read", "Chunk ID: read\nProcess exited with code 0\nOutput:\n# Demo\n"),
		callRaw("2026-07-09T00:00:05Z", "fc-edit", "call-edit", "apply_patch", "*** Begin Patch\n*** Update File: README.md\n@@\n-# Demo\n+# Demo updated\n*** End Patch\n"),
		output("2026-07-09T00:00:06Z", "call-edit", "Success. Updated the following files:\nM README.md\n"),
		call("2026-07-09T00:00:07Z", "fc-test", "call-test", "exec_command", map[string]any{
			"cmd":     "go test ./...",
			"workdir": filepath.ToSlash(root),
		}),
		output("2026-07-09T00:00:08Z", "call-test", "Chunk ID: test\nProcess exited with code 1\nOutput:\nFAIL ./...\n"),
	)

	adapter := Adapter{Dir: dir, IndexPath: index}
	meta, err := adapter.Summarize(session)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != "codex-demo" || meta.Harness != "codex" || meta.Title != "Codex demo trace" {
		t.Fatalf("meta = %#v", meta)
	}
	if meta.Model != "gpt-5.5" || meta.GitBranch != "main" || meta.EventCount != 3 {
		t.Fatalf("meta = %#v", meta)
	}

	trace, err := adapter.Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	if trace.Session.ID != "codex-demo" || trace.Session.Harness != "codex" || trace.Session.Commit != "abc123" {
		t.Fatalf("session = %#v", trace.Session)
	}
	if trace.Session.Title != "Codex demo trace" || trace.Session.Model != "gpt-5.5" {
		t.Fatalf("session = %#v", trace.Session)
	}
	if len(trace.Marks) != 1 || trace.Marks[0].Type != "user-message" || trace.Marks[0].Note != "inspect" {
		t.Fatalf("marks = %#v", trace.Marks)
	}
	if len(trace.Events) != 3 {
		t.Fatalf("events = %d", len(trace.Events))
	}
	if got := trace.Events[0]; got.Tool != "exec_command" || got.Action != "read" || len(got.Targets) != 1 || got.Targets[0].Path != "README.md" || got.Targets[0].Touch != "read" {
		t.Fatalf("read event = %#v", got)
	}
	if got := trace.Events[1]; got.Tool != "apply_patch" || got.Action != "edit" || got.Targets[0].Touch != "edit" {
		t.Fatalf("edit event = %#v", got)
	}
	if got := trace.Events[2]; got.Action != "verify" || !got.IsError {
		t.Fatalf("verify event = %#v", got)
	}
	if trace.Stats.Edited != 1 || trace.Stats.EventsBeforeFirstEdit != 1 || math.Abs(trace.Stats.ErrorRate-1.0/3.0) > 0.0001 {
		t.Fatalf("stats = %#v", trace.Stats)
	}
	want := model.Observability{Reads: model.ObservabilityEstimated, Errors: model.ObservabilityEstimated}
	if trace.Stats.Observability != want {
		t.Fatalf("observability = %#v", trace.Stats.Observability)
	}
}

func TestParseCodexContextCompactedBecomesCompactionMark(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "rollout-2026-07-09T00-00-00-codex-compact.jsonl")
	writeJSONL(t, session,
		map[string]any{
			"timestamp": "2026-07-09T00:00:00Z",
			"type":      "session_meta",
			"payload":   map[string]any{"id": "codex-compact", "timestamp": "2026-07-09T00:00:00Z"},
		},
		call("2026-07-09T00:00:01Z", "fc-1", "call-1", "exec_command", map[string]any{"cmd": "true"}),
		output("2026-07-09T00:00:02Z", "call-1", "Process exited with code 0"),
		map[string]any{
			"timestamp": "2026-07-09T00:00:03Z",
			"type":      "event_msg",
			"payload":   map[string]any{"type": "context_compacted"},
		},
	)

	trace, err := Adapter{Dir: dir}.Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Marks) != 1 || trace.Marks[0].Type != "compaction" || trace.Marks[0].Seq != 1 {
		t.Fatalf("marks = %#v", trace.Marks)
	}
	if trace.Stats.Compactions != 1 {
		t.Fatalf("compactions = %d", trace.Stats.Compactions)
	}
}

func TestParseCodexSpawnAgentBecomesSubagentMark(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "rollout-2026-07-14T00-00-00-codex-subagent.jsonl")
	writeJSONL(t, session,
		map[string]any{
			"timestamp": "2026-07-14T00:00:00Z",
			"type":      "session_meta",
			"payload":   map[string]any{"id": "codex-subagent", "timestamp": "2026-07-14T00:00:00Z"},
		},
		call("2026-07-14T00:00:01Z", "fc-1", "call-1", "exec_command", map[string]any{"cmd": "true"}),
		output("2026-07-14T00:00:02Z", "call-1", "Process exited with code 0"),
		call("2026-07-14T00:00:03Z", "fc-2", "call-2", "spawn_agent", map[string]any{
			"task_name": "qa",
			"message":   "Review the change",
		}),
		output("2026-07-14T00:00:04Z", "call-2", `{"agent_id":"agent-1"}`),
	)

	trace, err := Adapter{Dir: dir}.Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Marks) != 1 || trace.Marks[0].Type != "subagent" || trace.Marks[0].Seq != 1 || trace.Marks[0].Note != "spawn_agent" {
		t.Fatalf("marks = %#v", trace.Marks)
	}
	if trace.Stats.Subagents != 1 {
		t.Fatalf("subagents = %d", trace.Stats.Subagents)
	}
}

func TestParseCodexCustomCallsCanonicalizesOrderAndPatchResults(t *testing.T) {
	root := t.TempDir()
	readme := filepath.Join(root, "README.md")
	added := filepath.Join(root, "added.go")
	if err := os.WriteFile(readme, []byte("# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(t.TempDir(), "custom.jsonl")
	failed := false
	succeeded := true
	writeJSONL(t, session,
		map[string]any{
			"timestamp": "2026-07-10T00:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":  "custom-demo",
				"cwd": filepath.ToSlash(root),
			},
		},
		userMessage("2026-07-10T00:00:01Z", "before calls"),
		customCall("2026-07-10T00:00:02Z", "", "", "apply_patch", "*** Begin Patch\n"),
		customCall("2026-07-10T00:00:03Z", "ctc-patch", "call-patch", "apply_patch", "*** Begin Patch\n*** Update File: README.md\n*** End Patch\n"),
		userMessage("2026-07-10T00:00:04Z", "between call and output"),
		customOutput("2026-07-10T00:00:05Z", "call-late", "orphan output"),
		customCall("2026-07-10T00:00:06Z", "ctc-image", "call-image", "view_image", map[string]any{
			"path": filepath.ToSlash(readme),
		}),
		customOutput("2026-07-10T00:00:07Z", "call-image", []any{map[string]any{"text": "first image output"}}),
		customOutput("2026-07-10T00:00:08Z", "call-image", "Process exited with code 1"),
		customOutput("2026-07-10T00:00:09Z", "call-patch", "first patch output"),
		customOutput("2026-07-10T00:00:10Z", "call-patch", "second patch output"),
		patchApplyEnd("2026-07-10T00:00:11Z", "call-patch", &failed, map[string]string{
			filepath.ToSlash(readme): "update",
			filepath.ToSlash(added):  "add",
		}),
		patchApplyEnd("2026-07-10T00:00:12Z", "call-patch", &succeeded, map[string]string{
			filepath.Join(root, "ignored.go"): "add",
		}),
		patchApplyEnd("2026-07-10T00:00:13Z", "orphan-patch", &failed, map[string]string{
			filepath.Join(root, "orphan.go"): "add",
		}),
		call("2026-07-10T00:00:14Z", "fc-late", "call-late", "exec_command", map[string]any{
			"cmd":     "go test ./...",
			"workdir": filepath.ToSlash(root),
		}),
		userMessage("2026-07-10T00:00:15Z", "after calls"),
		customCall("2026-07-10T00:00:16Z", "duplicate", "call-patch", "view_image", map[string]any{
			"path": filepath.Join(root, "ignored.png"),
		}),
	)

	a := Adapter{}
	meta, err := a.Summarize(session)
	if err != nil {
		t.Fatal(err)
	}
	if meta.EventCount != 3 {
		t.Fatalf("event count = %d, want 3", meta.EventCount)
	}

	trace, err := a.Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	if trace.Session.EventCount != meta.EventCount || len(trace.Events) != 3 {
		t.Fatalf("meta events = %d, trace session events = %d, events = %d", meta.EventCount, trace.Session.EventCount, len(trace.Events))
	}
	if len(trace.Marks) != 3 || trace.Marks[0].Seq != 0 || trace.Marks[1].Seq != 1 || trace.Marks[2].Seq != 3 {
		t.Fatalf("marks = %#v", trace.Marks)
	}
	patchEvent := trace.Events[0]
	if patchEvent.Tool != "apply_patch" || patchEvent.Action != "edit" || !patchEvent.IsError || patchEvent.ResultBytes != len("first patch output") {
		t.Fatalf("patch event = %#v", patchEvent)
	}
	if len(patchEvent.Targets) != 2 || patchEvent.Targets[0].Path != "README.md" || patchEvent.Targets[1].Path != "added.go" {
		t.Fatalf("patch targets = %#v", patchEvent.Targets)
	}
	imageEvent := trace.Events[1]
	if imageEvent.Tool != "view_image" || imageEvent.IsError || imageEvent.ResultBytes != len("first image output") {
		t.Fatalf("image event = %#v", imageEvent)
	}
	if len(imageEvent.Targets) != 1 || imageEvent.Targets[0].Path != "README.md" {
		t.Fatalf("image targets = %#v", imageEvent.Targets)
	}
	lateEvent := trace.Events[2]
	if lateEvent.Tool != "exec_command" || lateEvent.Action != "verify" || lateEvent.ResultBytes != 0 || lateEvent.IsError {
		t.Fatalf("late event = %#v", lateEvent)
	}
}

func TestPatchApplyEndSuccessOverridesTextualFailure(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(t.TempDir(), "patch-success.jsonl")
	succeeded := true
	writeJSONL(t, session,
		map[string]any{
			"timestamp": "2026-07-10T00:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":  "patch-success",
				"cwd": filepath.ToSlash(root),
			},
		},
		customCall("2026-07-10T00:00:01Z", "ctc", "call-patch", "apply_patch", "*** Begin Patch\n*** Add File: added.go\n*** End Patch\n"),
		customOutput("2026-07-10T00:00:02Z", "call-patch", "Process exited with code 1"),
		patchApplyEnd("2026-07-10T00:00:03Z", "call-patch", &succeeded, map[string]string{
			filepath.Join(root, "added.go"): "add",
		}),
	)

	trace, err := (Adapter{}).Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Events) != 1 || trace.Events[0].IsError {
		t.Fatalf("events = %#v", trace.Events)
	}
}

func TestParseCodexCustomExec(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(t.TempDir(), "custom-exec.jsonl")
	script := `const r = await tools.exec_command({"cmd":"sed -n '1,20p' README.md","workdir":` + mustJSON(t, filepath.ToSlash(root)) + `}); text(r.output);`
	writeJSONL(t, session,
		map[string]any{
			"timestamp": "2026-07-10T00:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":  "custom-exec",
				"cwd": filepath.ToSlash(root),
			},
		},
		customCall("2026-07-10T00:00:01Z", "ctc", "call-exec", "exec", script),
		customOutput("2026-07-10T00:00:02Z", "call-exec", []any{map[string]any{"type": "input_text", "text": "# Demo\n"}}),
	)

	a := Adapter{}
	meta, err := a.Summarize(session)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := a.Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	if meta.EventCount != 1 || len(trace.Events) != 1 {
		t.Fatalf("meta=%d events=%d", meta.EventCount, len(trace.Events))
	}
	event := trace.Events[0]
	if event.Tool != "exec" || event.Action != "read" || len(event.Targets) != 1 || event.Targets[0].Path != "README.md" || event.Targets[0].Touch != "read" {
		t.Fatalf("event = %#v", event)
	}
}

func TestCallIDFallsBackToResponseID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(t.TempDir(), "fallback-id.jsonl")
	writeJSONL(t, session,
		map[string]any{
			"timestamp": "2026-07-10T00:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":  "fallback-session",
				"cwd": filepath.ToSlash(root),
			},
		},
		customCall("2026-07-10T00:00:01Z", "fallback-call", "", "view_image", map[string]any{"path": path}),
		customOutput("2026-07-10T00:00:02Z", "fallback-call", "ok"),
	)

	a := Adapter{}
	meta, err := a.Summarize(session)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := a.Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	if meta.EventCount != 1 || len(trace.Events) != 1 || trace.Events[0].ResultBytes != 2 {
		t.Fatalf("meta=%#v events=%#v", meta, trace.Events)
	}
}

func TestParseCodexJSRepl(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "packages", "db", "src", "index.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("export const db = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(t.TempDir(), "js-repl.jsonl")
	writeJSONL(t, session,
		map[string]any{
			"timestamp": "2026-07-10T00:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":  "js-repl",
				"cwd": filepath.ToSlash(root),
			},
		},
		customCall("2026-07-10T00:00:01Z", "ctc", "call-js", "js_repl", `await import("./packages/db/src/index.ts")`),
		customOutput("2026-07-10T00:00:02Z", "call-js", "loaded"),
	)

	trace, err := (Adapter{}).Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Events) != 1 {
		t.Fatalf("events = %#v", trace.Events)
	}
	event := trace.Events[0]
	if event.Tool != "js_repl" || event.Action != "exec" || len(event.Targets) != 1 || event.Targets[0].Path != "packages/db/src/index.ts" {
		t.Fatalf("event = %#v", event)
	}
}

func TestCommandOutputFailedVariants(t *testing.T) {
	tests := []struct {
		output string
		want   bool
	}{
		{output: "Process exited with code 1", want: true},
		{output: "Exit code: 2", want: true},
		{output: `{"output":"ok","metadata":{"exit_code":0}}`, want: false},
		{output: `{"output":"failed","metadata":{"exit_code":1}}`, want: true},
		{output: `{"output":"failed","exit_code":3}`, want: true},
		{output: `{"output":"Exit code: 1","metadata":{"exit_code":0}}`, want: false},
		{output: "Script completed\nWall time 0.1 seconds\nOutput:\nExit code: 1", want: false},
		{output: "Script running with cell ID 28\nExit code: 1", want: false},
		{output: "Script failed\nExit code: 0", want: true},
		{output: "plain output", want: false},
		{output: `{"message":"Wait timed out after 20000ms","timed_out":true}`, want: true},
		{output: `{"message":"still running","timed_out":false}`, want: false},
		{output: "apply_patch verification failed: Failed to find expected lines in a.go", want: true},
		{output: "Wall time: 8.9 seconds\naborted by user", want: true},
		{output: "Wall time: 8.9 seconds\nOutput:\naborted by user", want: false},
	}
	for _, tt := range tests {
		if got := commandOutputFailed(tt.output); got != tt.want {
			t.Errorf("commandOutputFailed(%q) = %v, want %v", tt.output, got, tt.want)
		}
	}
}

func TestListSessionsSkipsNonCodexJSONL(t *testing.T) {
	dir := t.TempDir()
	writeJSONL(t, filepath.Join(dir, "not-codex.jsonl"), map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role":    "assistant",
			"content": "hello",
		},
	})
	writeJSONL(t, filepath.Join(dir, "codex.jsonl"), map[string]any{
		"timestamp": "2026-07-09T00:00:00Z",
		"type":      "session_meta",
		"payload": map[string]any{
			"id":  "codex-list",
			"cwd": "/tmp",
		},
	})

	sessions, err := (Adapter{Dir: dir}).ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "codex-list" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestListSessionsSkipsCodexSubagents(t *testing.T) {
	dir := t.TempDir()
	mainSession := filepath.Join(dir, "main.jsonl")
	legacySession := filepath.Join(dir, "legacy.jsonl")
	subagentSession := filepath.Join(dir, "subagent.jsonl")
	transitionalSubagentSession := filepath.Join(dir, "transitional-subagent.jsonl")
	writeJSONL(t, mainSession,
		map[string]any{
			"timestamp": "2026-07-10T00:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":             "main-thread",
				"cwd":            "/tmp",
				"thread_source":  "user",
				"source":         "vscode",
				"forked_from_id": "user-owned-parent",
			},
		},
		// Only the first valid session_meta controls visibility. Later context
		// records must not turn a main thread into an auxiliary session.
		map[string]any{
			"timestamp": "2026-07-10T00:00:00.500Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":  "replayed-child-context",
				"cwd": "/tmp",
				"source": map[string]any{
					"subagent": "replayed-child-context",
				},
			},
		},
	)
	writeJSONL(t, subagentSession,
		map[string]any{
			"timestamp": "2026-07-10T00:00:01Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":  "child-thread",
				"cwd": "/tmp",
				"source": map[string]any{
					"subagent": map[string]any{
						"thread_spawn": map[string]any{"parent_thread_id": "main-thread"},
					},
				},
			},
		},
		// Forked context can replay parent metadata. The first record remains
		// authoritative for deciding whether this file belongs in the list.
		map[string]any{
			"timestamp": "2026-07-10T00:00:02Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":     "main-thread",
				"cwd":    "/tmp",
				"source": "vscode",
			},
		},
		call("2026-07-10T00:00:03Z", "fc-child", "call-child", "exec_command", map[string]any{
			"cmd":     "pwd",
			"workdir": "/tmp",
		}),
		output("2026-07-10T00:00:04Z", "call-child", "Chunk ID: child\nProcess exited with code 0\nOutput:\n/tmp\n"),
	)
	writeJSONL(t, transitionalSubagentSession, map[string]any{
		"timestamp": "2026-07-10T00:00:03Z",
		"type":      "session_meta",
		"payload": map[string]any{
			"id":            "transitional-child",
			"cwd":           "/tmp",
			"thread_source": "subagent",
			"source":        "vscode",
		},
	})
	writeJSONL(t, legacySession, map[string]any{
		"id":        "legacy-main",
		"timestamp": "2025-06-13T14:42:30.751Z",
	})

	adapter := Adapter{Dir: dir}
	mainMeta, err := adapter.Summarize(mainSession)
	if err != nil || mainMeta.Auxiliary {
		t.Fatalf("main meta = %#v, err = %v", mainMeta, err)
	}
	subagentMeta, err := adapter.Summarize(subagentSession)
	if err != nil || !subagentMeta.Auxiliary || subagentMeta.ID != "child-thread" || subagentMeta.EventCount != 1 {
		t.Fatalf("subagent meta = %#v, err = %v", subagentMeta, err)
	}
	subagentTrace, err := adapter.Parse(subagentSession)
	if err != nil {
		t.Fatal(err)
	}
	if subagentTrace.Session.ID != subagentMeta.ID || subagentTrace.Session.Title != subagentMeta.Title ||
		subagentTrace.Session.EndedAt != subagentMeta.EndedAt || subagentTrace.Session.EventCount != subagentMeta.EventCount {
		t.Fatalf("subagent summary/trace mismatch: meta=%#v trace=%#v", subagentMeta, subagentTrace.Session)
	}
	transitionalMeta, err := adapter.Summarize(transitionalSubagentSession)
	if err != nil || !transitionalMeta.Auxiliary {
		t.Fatalf("transitional subagent meta = %#v, err = %v", transitionalMeta, err)
	}
	sessions, err := adapter.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %#v", sessions)
	}
	visible := map[string]string{}
	for _, session := range sessions {
		visible[session.ID] = session.Path
	}
	if visible["main-thread"] != mainSession || visible["legacy-main"] != legacySession {
		t.Fatalf("visible sessions = %#v", visible)
	}
}

func TestSummarizePopulatesSubagentMetadata(t *testing.T) {
	session := filepath.Join(t.TempDir(), "subagent.jsonl")
	writeJSONL(t, session, map[string]any{
		"timestamp": "2026-07-10T00:00:01Z",
		"type":      "session_meta",
		"payload": map[string]any{
			"id":            "child-thread",
			"session_id":    "root-thread",
			"thread_source": "subagent",
			"source": map[string]any{
				"subagent": map[string]any{
					"thread_spawn": map[string]any{
						"parent_thread_id": "parent-thread",
						"depth":            2,
						"agent_nickname":   "Tesla",
						"agent_role":       "explorer",
					},
				},
			},
		},
	})

	meta, err := (Adapter{}).Summarize(session)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Auxiliary || meta.Agent == nil {
		t.Fatalf("meta = %#v", meta)
	}
	want := model.AgentSessionMeta{
		SourceID:        "child-thread",
		RootSessionID:   "root-thread",
		ParentSessionID: "parent-thread",
		Depth:           2,
		Label:           "Tesla",
		Role:            "explorer",
	}
	if *meta.Agent != want {
		t.Fatalf("agent meta = %#v, want %#v", *meta.Agent, want)
	}
}

func TestSessionMetaPayloadDetectsSubagentFormats(t *testing.T) {
	tests := []struct {
		name    string
		payload sessionMetaPayload
		want    bool
	}{
		{name: "thread source", payload: sessionMetaPayload{ThreadSource: "subagent"}, want: true},
		{name: "legacy object", payload: sessionMetaPayload{Source: json.RawMessage(`{"subagent":{"thread_spawn":{}}}`)}, want: true},
		{name: "legacy scalar", payload: sessionMetaPayload{Source: json.RawMessage(`{"subagent":"memory_consolidation"}`)}, want: true},
		{name: "null subagent", payload: sessionMetaPayload{Source: json.RawMessage(`{"subagent":null}`)}, want: false},
		{name: "top level source", payload: sessionMetaPayload{Source: json.RawMessage(`"vscode"`)}, want: false},
		{name: "unknown object", payload: sessionMetaPayload{Source: json.RawMessage(`{"other":"user"}`)}, want: false},
		{name: "malformed source", payload: sessionMetaPayload{Source: json.RawMessage(`{`)}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.payload.isSubagent(); got != tt.want {
				t.Fatalf("isSubagent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func call(ts, id, callID, name string, args map[string]any) map[string]any {
	b, _ := json.Marshal(args)
	return callRaw(ts, id, callID, name, string(b))
}

func callRaw(ts, id, callID, name, args string) map[string]any {
	return map[string]any{
		"timestamp": ts,
		"type":      "response_item",
		"payload": map[string]any{
			"type":      "function_call",
			"id":        id,
			"name":      name,
			"arguments": args,
			"call_id":   callID,
		},
	}
}

func output(ts, callID, text string) map[string]any {
	return map[string]any{
		"timestamp": ts,
		"type":      "response_item",
		"payload": map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  text,
		},
	}
}

func customCall(ts, id, callID, name string, input any) map[string]any {
	return map[string]any{
		"timestamp": ts,
		"type":      "response_item",
		"payload": map[string]any{
			"type":    "custom_tool_call",
			"id":      id,
			"name":    name,
			"input":   input,
			"call_id": callID,
		},
	}
}

func customOutput(ts, callID string, value any) map[string]any {
	return map[string]any{
		"timestamp": ts,
		"type":      "response_item",
		"payload": map[string]any{
			"type":    "custom_tool_call_output",
			"call_id": callID,
			"output":  value,
		},
	}
}

func userMessage(ts, text string) map[string]any {
	return map[string]any{
		"timestamp": ts,
		"type":      "response_item",
		"payload": map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": text}},
		},
	}
}

func patchApplyEnd(ts, callID string, success *bool, changes map[string]string) map[string]any {
	payloadChanges := make(map[string]any, len(changes))
	for path, changeType := range changes {
		payloadChanges[path] = map[string]any{"type": changeType}
	}
	return map[string]any{
		"timestamp": ts,
		"type":      "event_msg",
		"payload": map[string]any{
			"type":    "patch_apply_end",
			"call_id": callID,
			"success": success,
			"changes": payloadChanges,
		},
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func writeJSONL(t *testing.T, path string, values ...any) {
	t.Helper()
	content := ""
	for _, value := range values {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		content += string(b) + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSummarizeMarksJudgeWorkdirAuxiliary(t *testing.T) {
	workdir := judge.WorkDir()
	if workdir == "" {
		t.Skip("no home directory available")
	}
	dir := t.TempDir()
	session := filepath.Join(dir, "rollout-2026-07-14T00-00-00-judge-run.jsonl")
	writeJSONL(t, session,
		map[string]any{
			"timestamp": "2026-07-14T00:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":         "judge-run",
				"session_id": "judge-run",
				"timestamp":  "2026-07-14T00:00:00Z",
				"cwd":        workdir,
			},
		},
	)
	adapter := Adapter{Dir: dir}
	meta, err := adapter.Summarize(session)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Auxiliary {
		t.Fatalf("session recorded in judge workdir %s should be auxiliary", workdir)
	}
	metas, err := adapter.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range metas {
		if m.ID == "judge-run" {
			t.Fatal("judge session leaked into ListSessions")
		}
	}
}

func TestParseSkipsInjectedUserMessages(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "rollout-2026-07-14T00-00-01-injected.jsonl")
	userMessage := func(text string) map[string]any {
		return map[string]any{
			"timestamp": "2026-07-14T00:00:01Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": text}},
			},
		}
	}
	writeJSONL(t, session,
		map[string]any{
			"timestamp": "2026-07-14T00:00:00Z",
			"type":      "session_meta",
			"payload":   map[string]any{"id": "injected", "session_id": "injected", "timestamp": "2026-07-14T00:00:00Z", "cwd": "/repo"},
		},
		userMessage("# AGENTS.md instructions for /repo\n\nproject rules"),
		userMessage("<environment_context>shell: zsh</environment_context>"),
		userMessage("the real task"),
	)
	trace, err := (Adapter{Dir: dir}).Parse(session)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, mark := range trace.Marks {
		if mark.Type == "user-message" {
			notes = append(notes, mark.Note)
		}
	}
	// Injected AGENTS.md and markup wrappers stay out of marks — they would
	// inflate userTurns and pose as the task in judge input.
	if len(notes) != 1 || notes[0] != "the real task" {
		t.Fatalf("user-message notes = %#v", notes)
	}
	if trace.Stats.UserTurns != 1 {
		t.Fatalf("userTurns = %d, want 1", trace.Stats.UserTurns)
	}
	// Summarize must count the same turns as the parse, or the rail badge's
	// staleness check would disagree with the report.
	meta, err := (Adapter{Dir: dir}).Summarize(session)
	if err != nil {
		t.Fatal(err)
	}
	if meta.UserTurns != 1 {
		t.Fatalf("summarized userTurns = %d, want 1", meta.UserTurns)
	}
}
