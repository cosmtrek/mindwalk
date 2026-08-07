package pi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/adapter/claudecode"
	"github.com/cosmtrek/mindwalk/internal/adapter/codex"
)

func writeSession(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-07-21T14-34-07-238Z_test-session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func header(cwd string) string {
	return fmt.Sprintf(`{"type":"session","version":3,"id":"sess-1","timestamp":"2026-07-21T14:34:07.238Z","cwd":%q}`, cwd)
}

func TestParseLinearSession(t *testing.T) {
	root := t.TempDir()
	mainGo := filepath.Join(root, "src", "main.go")
	path := writeSession(t,
		header(root),
		`{"type":"model_change","id":"e1","parentId":null,"timestamp":"2026-07-21T14:34:07.287Z","provider":"moonshotai","modelId":"kimi-k3"}`,
		`{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-07-21T14:34:22.963Z","message":{"role":"user","content":[{"type":"text","text":"fix the bug"}],"timestamp":1784644462960}}`,
		fmt.Sprintf(`{"type":"message","id":"e3","parentId":"e2","timestamp":"2026-07-21T14:34:25.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"read","arguments":{"path":%q}}],"model":"kimi-k3","stopReason":"toolUse"}}`, mainGo),
		`{"type":"message","id":"e4","parentId":"e3","timestamp":"2026-07-21T14:34:26.000Z","message":{"role":"toolResult","toolCallId":"t1","toolName":"read","content":[{"type":"text","text":"package main"}],"isError":false}}`,
		fmt.Sprintf(`{"type":"message","id":"e5","parentId":"e4","timestamp":"2026-07-21T14:34:27.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t2","name":"edit","arguments":{"path":%q,"edits":[{"oldText":"a","newText":"b"}]}}],"model":"kimi-k3","stopReason":"toolUse"}}`, mainGo),
		`{"type":"message","id":"e6","parentId":"e5","timestamp":"2026-07-21T14:34:28.000Z","message":{"role":"toolResult","toolCallId":"t2","toolName":"edit","content":[{"type":"text","text":"oldText not found"}],"isError":true}}`,
	)

	trace, err := Adapter{}.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if trace.Session.Harness != "pi" || trace.Session.ID != "sess-1" || trace.Session.Cwd != root {
		t.Fatalf("session = %#v", trace.Session)
	}
	if trace.Session.Model != "kimi-k3" {
		t.Fatalf("model = %q", trace.Session.Model)
	}
	if trace.Session.Title != "fix the bug" {
		t.Fatalf("title = %q", trace.Session.Title)
	}
	if trace.Session.StartedAt != "2026-07-21T14:34:07.238Z" || trace.Session.EndedAt != "2026-07-21T14:34:28.000Z" {
		t.Fatalf("times = %q..%q", trace.Session.StartedAt, trace.Session.EndedAt)
	}
	if len(trace.Events) != 2 || trace.Session.EventCount != 2 {
		t.Fatalf("events = %#v", trace.Events)
	}
	read := trace.Events[0]
	if read.Tool != "read" || read.Action != "read" || read.IsError {
		t.Fatalf("read event = %#v", read)
	}
	if len(read.Targets) != 1 || read.Targets[0].Path != "src/main.go" || read.Targets[0].Touch != "read" {
		t.Fatalf("read targets = %#v", read.Targets)
	}
	edit := trace.Events[1]
	if edit.Tool != "edit" || edit.Action != "edit" || !edit.IsError {
		t.Fatalf("edit event = %#v", edit)
	}
	if len(trace.Marks) != 1 || trace.Marks[0].Type != "user-message" || trace.Marks[0].Note != "fix the bug" {
		t.Fatalf("marks = %#v", trace.Marks)
	}
	if trace.Stats.UserTurns != 1 || trace.Stats.Errors.Edit != 1 {
		t.Fatalf("stats = %#v", trace.Stats)
	}
	if trace.Stats.Observability.Errors != "exact" {
		t.Fatalf("observability = %#v", trace.Stats.Observability)
	}
}

func TestParsePreservesToolOutcomeCertainty(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t,
		header(root),
		`{"type":"message","id":"a1","parentId":null,"timestamp":"2026-07-21T14:35:01.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"success","name":"read","arguments":{"path":"ok.go"}},{"type":"toolCall","id":"failure","name":"edit","arguments":{"path":"bad.go"}},{"type":"toolCall","id":"unknown","name":"bash","arguments":{"command":"sleep 100"}},{"type":"toolCall","id":"wrong-case","name":"edit","arguments":{"path":"case.go"}}],"stopReason":"toolUse"}}`,
		`{"type":"message","id":"r1","parentId":"a1","timestamp":"2026-07-21T14:35:02.000Z","message":{"role":"toolResult","toolCallId":"success","toolName":"read","content":[{"type":"text","text":"ok"}],"isError":false}}`,
		`{"type":"message","id":"r2","parentId":"r1","timestamp":"2026-07-21T14:35:03.000Z","message":{"role":"toolResult","toolCallId":"failure","toolName":"edit","content":[{"type":"text","text":"failed"}],"isError":true}}`,
		`{"type":"message","id":"r3","parentId":"r2","timestamp":"2026-07-21T14:35:04.000Z","message":{"role":"toolResult","toolCallId":"unknown","toolName":"bash","content":[{"type":"text","text":"still running"}]}}`,
		`{"type":"message","id":"r4","parentId":"r3","timestamp":"2026-07-21T14:35:05.000Z","message":{"role":"toolResult","toolCallId":"wrong-case","toolName":"edit","content":[{"type":"text","text":"ok"}],"IsError":true}}`,
	)

	trace, err := (Adapter{}).Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Events) != 4 {
		t.Fatalf("events = %#v", trace.Events)
	}
	if got := trace.Events[0]; !got.OutcomeKnown || got.IsError {
		t.Fatalf("success event = %#v", got)
	}
	if got := trace.Events[1]; !got.OutcomeKnown || !got.IsError {
		t.Fatalf("failure event = %#v", got)
	}
	if got := trace.Events[2]; got.OutcomeKnown || got.IsError {
		t.Fatalf("unknown event = %#v", got)
	}
	if got := trace.Events[3]; got.OutcomeKnown || got.IsError {
		t.Fatalf("wrong-case error key event = %#v", got)
	}
	if trace.Stats.Observability.Errors != "estimated" {
		t.Fatalf("observability = %#v", trace.Stats.Observability)
	}
}

func TestParseFollowsTrunkAcrossBranches(t *testing.T) {
	root := t.TempDir()
	fileA := filepath.Join(root, "a.go")
	fileB := filepath.Join(root, "b.go")
	path := writeSession(t,
		header(root),
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-07-21T14:35:00.000Z","message":{"role":"user","content":[{"type":"text","text":"try something"}],"timestamp":1}}`,
		fmt.Sprintf(`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-07-21T14:35:01.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"write","arguments":{"path":%q,"content":"x"}}],"stopReason":"toolUse"}}`, fileA),
		`{"type":"message","id":"r1","parentId":"a1","timestamp":"2026-07-21T14:35:02.000Z","message":{"role":"toolResult","toolCallId":"t1","toolName":"write","content":[{"type":"text","text":"ok"}],"isError":false}}`,
		`{"type":"branch_summary","id":"b1","parentId":"u1","timestamp":"2026-07-21T14:36:00.000Z","fromId":"r1","summary":"abandoned the first approach"}`,
		fmt.Sprintf(`{"type":"message","id":"a2","parentId":"b1","timestamp":"2026-07-21T14:36:01.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t2","name":"write","arguments":{"path":%q,"content":"y"}}],"stopReason":"toolUse"}}`, fileB),
		`{"type":"message","id":"r2","parentId":"a2","timestamp":"2026-07-21T14:36:02.000Z","message":{"role":"toolResult","toolCallId":"t2","toolName":"write","content":[{"type":"text","text":"ok"}],"isError":false}}`,
	)

	trace, err := Adapter{}.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Events) != 1 {
		t.Fatalf("events = %#v", trace.Events)
	}
	if got := trace.Events[0].Targets[0].Path; got != "b.go" {
		t.Fatalf("target = %q, want the trunk branch's file", got)
	}
	var types []string
	for _, mark := range trace.Marks {
		types = append(types, mark.Type)
	}
	if len(trace.Marks) != 2 || trace.Marks[0].Type != "user-message" || trace.Marks[1].Type != "compaction" {
		t.Fatalf("marks = %v", types)
	}
	if !strings.HasPrefix(trace.Marks[1].Note, "branch: ") {
		t.Fatalf("branch note = %q", trace.Marks[1].Note)
	}
}

func TestParseV1LinearWithoutIDs(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t,
		fmt.Sprintf(`{"type":"session","version":1,"id":"sess-v1","timestamp":"2026-07-21T14:34:07.238Z","cwd":%q}`, root),
		`{"type":"message","timestamp":"2026-07-21T14:34:22.000Z","message":{"role":"user","content":"hello","timestamp":1}}`,
		fmt.Sprintf(`{"type":"message","timestamp":"2026-07-21T14:34:23.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"read","arguments":{"path":%q}}],"stopReason":"toolUse"}}`, filepath.Join(root, "x.go")),
		`{"type":"message","timestamp":"2026-07-21T14:34:24.000Z","message":{"role":"toolResult","toolCallId":"t1","toolName":"read","content":[{"type":"text","text":"data"}],"isError":false}}`,
	)

	trace, err := Adapter{}.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Events) != 1 || trace.Events[0].Tool != "read" {
		t.Fatalf("events = %#v", trace.Events)
	}
	if len(trace.Marks) != 1 || trace.Marks[0].Type != "user-message" {
		t.Fatalf("marks = %#v", trace.Marks)
	}
}

func TestParseRejectsOtherSources(t *testing.T) {
	claudeFile := writeSession(t,
		`{"type":"user","sessionId":"c1","timestamp":"2026-07-21T14:34:07.238Z","cwd":"/tmp","message":{"role":"user","content":"hi"}}`,
	)
	codexFile := writeSession(t,
		`{"type":"session_meta","payload":{"id":"x","timestamp":"2026-07-21T14:34:07.238Z","cwd":"/tmp"}}`,
	)
	for _, path := range []string{claudeFile, codexFile} {
		if _, err := (Adapter{}).Parse(path); err == nil || !strings.Contains(err.Error(), "not a pi session") {
			t.Fatalf("Parse(%s) err = %v", filepath.Base(path), err)
		}
		if _, err := (Adapter{}).Summarize(path); err == nil {
			t.Fatalf("Summarize(%s) accepted a foreign session", filepath.Base(path))
		}
	}

	empty := writeSession(t)
	if _, err := (Adapter{}).Parse(empty); err == nil {
		t.Fatal("Parse accepted an empty file")
	}

	// The reverse direction: the other adapters must not claim a pi file,
	// or trial-parse dispatch would route pi sessions to the wrong source.
	piFile := writeSession(t,
		header(t.TempDir()),
		`{"type":"message","id":"e1","parentId":null,"timestamp":"2026-07-21T14:34:22.963Z","message":{"role":"user","content":[{"type":"text","text":"hello"}],"timestamp":1}}`,
	)
	if _, err := (claudecode.Adapter{}).Parse(piFile); err == nil {
		t.Fatal("claudecode adapter claimed a pi session")
	}
	if _, err := (codex.Adapter{}).Parse(piFile); err == nil {
		t.Fatal("codex adapter claimed a pi session")
	}
}

func TestParseBashExecution(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t,
		header(root),
		`{"type":"message","id":"e1","parentId":null,"timestamp":"2026-07-21T14:35:00.000Z","message":{"role":"bashExecution","command":"npm run build","output":"boom","exitCode":1,"cancelled":false,"truncated":false,"timestamp":1}}`,
		`{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-07-21T14:35:10.000Z","message":{"role":"bashExecution","command":"go test ./...","output":"ok","exitCode":0,"cancelled":false,"truncated":false,"timestamp":2}}`,
		`{"type":"message","id":"e3","parentId":"e2","timestamp":"2026-07-21T14:35:20.000Z","message":{"role":"bashExecution","command":"sleep 100","output":"","cancelled":true,"truncated":false,"timestamp":3}}`,
		`{"type":"message","id":"e4","parentId":"e3","timestamp":"2026-07-21T14:35:30.000Z","message":{"role":"bashExecution","command":"false","output":"boom","ExitCode":1,"cancelled":false,"truncated":false,"timestamp":4}}`,
	)

	trace, err := Adapter{}.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Events) != 4 {
		t.Fatalf("events = %#v", trace.Events)
	}
	if trace.Events[0].Tool != "bash" || !trace.Events[0].OutcomeKnown || !trace.Events[0].IsError {
		t.Fatalf("failed command event = %#v", trace.Events[0])
	}
	if trace.Events[1].Action != "verify" || !trace.Events[1].OutcomeKnown || trace.Events[1].IsError {
		t.Fatalf("verify event = %#v", trace.Events[1])
	}
	if trace.Events[2].OutcomeKnown || trace.Events[2].IsError {
		t.Fatalf("cancelled command without exit code has unknown outcome: %#v", trace.Events[2])
	}
	if trace.Events[3].OutcomeKnown || trace.Events[3].IsError {
		t.Fatalf("wrong-case exit code has unknown outcome: %#v", trace.Events[3])
	}
}

func TestParseExtensionChannels(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t,
		header(root),
		`{"type":"custom","id":"e1","parentId":null,"timestamp":"2026-07-21T14:35:00.000Z","customType":"my-state","data":{"count":42}}`,
		`{"type":"custom_message","id":"e2","parentId":"e1","timestamp":"2026-07-21T14:35:01.000Z","customType":"handoff","content":"resume from the previous session","display":true}`,
		`{"type":"message","id":"e3","parentId":"e2","timestamp":"2026-07-21T14:35:02.000Z","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>injected</system-reminder>"}],"timestamp":1}}`,
		`{"type":"message","id":"e4","parentId":"e3","timestamp":"2026-07-21T14:35:03.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"my_tool","arguments":{"action":"list"}}],"stopReason":"toolUse"}}`,
		`{"type":"message","id":"e5","parentId":"e4","timestamp":"2026-07-21T14:35:04.000Z","message":{"role":"toolResult","toolCallId":"t1","toolName":"my_tool","content":[{"type":"text","text":"done"}],"isError":false}}`,
	)

	trace, err := Adapter{}.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Marks) != 0 {
		t.Fatalf("extension channels must not produce marks: %#v", trace.Marks)
	}
	if len(trace.Events) != 1 {
		t.Fatalf("events = %#v", trace.Events)
	}
	event := trace.Events[0]
	if event.Tool != "my_tool" || event.Action != "other" || len(event.Targets) != 0 {
		t.Fatalf("extension tool event = %#v", event)
	}
	if trace.Stats.UserTurns != 0 {
		t.Fatalf("injected message counted as a user turn: %#v", trace.Stats)
	}
}

func TestParseFlushesUnpairedToolCalls(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t,
		header(root),
		fmt.Sprintf(`{"type":"message","id":"e1","parentId":null,"timestamp":"2026-07-21T14:35:00.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"read","arguments":{"path":%q}}],"stopReason":"aborted"}}`, filepath.Join(root, "x.go")),
	)

	trace, err := Adapter{}.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Events) != 1 || trace.Events[0].ResultBytes != 0 || trace.Events[0].IsError {
		t.Fatalf("events = %#v", trace.Events)
	}
}

func TestParseCompactionMark(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t,
		header(root),
		`{"type":"message","id":"e1","parentId":null,"timestamp":"2026-07-21T14:35:00.000Z","message":{"role":"user","content":"long task","timestamp":1}}`,
		`{"type":"compaction","id":"e2","parentId":"e1","timestamp":"2026-07-21T15:00:00.000Z","summary":"did things","firstKeptEntryId":"e1","tokensBefore":50000}`,
	)

	trace, err := Adapter{}.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Marks) != 2 || trace.Marks[1].Type != "compaction" {
		t.Fatalf("marks = %#v", trace.Marks)
	}
	if trace.Stats.Compactions != 1 {
		t.Fatalf("stats = %#v", trace.Stats)
	}
}

func TestSummarize(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t,
		header(root),
		`{"type":"model_change","id":"e1","parentId":null,"timestamp":"2026-07-21T14:34:07.287Z","provider":"moonshotai","modelId":"kimi-k3"}`,
		`{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-07-21T14:34:22.963Z","message":{"role":"user","content":[{"type":"text","text":"first task"}],"timestamp":1}}`,
		`{"type":"message","id":"e3","parentId":"e2","timestamp":"2026-07-21T14:34:25.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"bash","arguments":{"command":"ls"}}],"model":"kimi-k3","stopReason":"toolUse"}}`,
		`{"type":"message","id":"e4","parentId":"e3","timestamp":"2026-07-21T14:34:26.000Z","message":{"role":"toolResult","toolCallId":"t1","toolName":"bash","content":[{"type":"text","text":"ok"}],"isError":false}}`,
		`{"type":"message","id":"e5","parentId":"e4","timestamp":"2026-07-21T14:34:27.000Z","message":{"role":"user","content":[{"type":"text","text":"<turn_aborted>x</turn_aborted>"}],"timestamp":2}}`,
		`{"type":"session_info","id":"e6","parentId":"e5","timestamp":"2026-07-21T14:34:28.000Z","name":"Named session"}`,
	)

	meta, err := Adapter{}.Summarize(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Harness != "pi" || meta.ID != "sess-1" || meta.Cwd != root {
		t.Fatalf("meta = %#v", meta)
	}
	if meta.Title != "Named session" {
		t.Fatalf("title = %q", meta.Title)
	}
	if meta.Model != "kimi-k3" {
		t.Fatalf("model = %q", meta.Model)
	}
	if meta.EventCount != 1 || meta.UserTurns != 1 {
		t.Fatalf("counts = %d events, %d turns", meta.EventCount, meta.UserTurns)
	}
	if meta.StartedAt != "2026-07-21T14:34:07.238Z" || meta.EndedAt != "2026-07-21T14:34:28.000Z" {
		t.Fatalf("times = %q..%q", meta.StartedAt, meta.EndedAt)
	}
	if !strings.HasPrefix(meta.Key, "pi-") {
		t.Fatalf("key = %q", meta.Key)
	}
}

func TestSummarizeTitleFallsBackToFirstUserMessage(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t,
		header(root),
		`{"type":"message","id":"e1","parentId":null,"timestamp":"2026-07-21T14:34:22.963Z","message":{"role":"user","content":[{"type":"text","text":"rename the config loader"}],"timestamp":1}}`,
	)

	meta, err := Adapter{}.Summarize(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "rename the config loader" {
		t.Fatalf("title = %q", meta.Title)
	}
}

func TestListSessions(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "--Users-me-Developer-proj--")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	session := header("/Users/me/Developer/proj") + "\n" +
		`{"type":"message","id":"e1","parentId":null,"timestamp":"2026-07-21T14:34:22.963Z","message":{"role":"user","content":"hi","timestamp":1}}` + "\n"
	if err := os.WriteFile(filepath.Join(project, "a.jsonl"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "foreign.jsonl"), []byte(`{"type":"session_meta","payload":{}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	metas, err := Adapter{Dir: dir}.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != "sess-1" {
		t.Fatalf("metas = %#v", metas)
	}
}

func TestParseAcceptsCwdlessLegacySession(t *testing.T) {
	// pi's own readSessionHeader only requires type "session" plus a string
	// id; legacy sessions can lack a cwd entirely and must still open.
	noCwdField := writeSession(t,
		`{"type":"session","version":1,"id":"legacy-1","timestamp":"2026-07-21T14:34:07.238Z"}`,
		`{"type":"message","timestamp":"2026-07-21T14:34:22.000Z","message":{"role":"user","content":"hello","timestamp":1}}`,
	)
	emptyCwd := writeSession(t,
		`{"type":"session","version":3,"id":"legacy-2","timestamp":"2026-07-21T14:34:07.238Z","cwd":""}`,
		`{"type":"message","id":"e1","parentId":null,"timestamp":"2026-07-21T14:34:22.000Z","message":{"role":"user","content":"hello","timestamp":1}}`,
	)
	for _, path := range []string{noCwdField, emptyCwd} {
		trace, err := (Adapter{}).Parse(path)
		if err != nil {
			t.Fatalf("Parse(%s) rejected a legacy session: %v", filepath.Base(path), err)
		}
		if trace.Session.Cwd != "" {
			t.Fatalf("cwd = %q, want empty", trace.Session.Cwd)
		}
		meta, err := (Adapter{}).Summarize(path)
		if err != nil {
			t.Fatalf("Summarize(%s) rejected a legacy session: %v", filepath.Base(path), err)
		}
		if meta.UserTurns != 1 {
			t.Fatalf("meta = %#v", meta)
		}
	}

	headerWithoutID := writeSession(t,
		`{"type":"session","version":3,"timestamp":"2026-07-21T14:34:07.238Z","cwd":"/tmp"}`,
	)
	if _, err := (Adapter{}).Parse(headerWithoutID); err == nil {
		t.Fatal("Parse accepted a header without an id")
	}
}

func TestSummarizeMatchesParseOnBranchedSession(t *testing.T) {
	root := t.TempDir()
	fileA := filepath.Join(root, "a.go")
	fileB := filepath.Join(root, "b.go")
	path := writeSession(t,
		header(root),
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-07-21T14:35:00.000Z","message":{"role":"user","content":[{"type":"text","text":"try something"}],"timestamp":1}}`,
		fmt.Sprintf(`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-07-21T14:35:01.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"write","arguments":{"path":%q,"content":"x"}}],"stopReason":"toolUse"}}`, fileA),
		`{"type":"message","id":"r1","parentId":"a1","timestamp":"2026-07-21T14:35:02.000Z","message":{"role":"toolResult","toolCallId":"t1","toolName":"write","content":[{"type":"text","text":"ok"}],"isError":false}}`,
		`{"type":"message","id":"u2","parentId":"r1","timestamp":"2026-07-21T14:35:03.000Z","message":{"role":"user","content":[{"type":"text","text":"abandoned follow-up"}],"timestamp":2}}`,
		`{"type":"branch_summary","id":"b1","parentId":"u1","timestamp":"2026-07-21T14:36:00.000Z","fromId":"u2","summary":"left the first attempt"}`,
		fmt.Sprintf(`{"type":"message","id":"a2","parentId":"b1","timestamp":"2026-07-21T14:36:01.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t2","name":"write","arguments":{"path":%q,"content":"y"}}],"stopReason":"toolUse"}}`, fileB),
		`{"type":"message","id":"r2","parentId":"a2","timestamp":"2026-07-21T14:36:02.000Z","message":{"role":"toolResult","toolCallId":"t2","toolName":"write","content":[{"type":"text","text":"ok"}],"isError":false}}`,
	)

	trace, err := (Adapter{}).Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := (Adapter{}).Summarize(path)
	if err != nil {
		t.Fatal(err)
	}
	// The report badge compares these against the trace; counting abandoned
	// branches would mark every fresh report on a branched session stale.
	if meta.EventCount != trace.Session.EventCount {
		t.Fatalf("summary events = %d, trace events = %d", meta.EventCount, trace.Session.EventCount)
	}
	if meta.UserTurns != trace.Stats.UserTurns {
		t.Fatalf("summary turns = %d, trace turns = %d", meta.UserTurns, trace.Stats.UserTurns)
	}
	if meta.Title != trace.Session.Title {
		t.Fatalf("summary title = %q, trace title = %q", meta.Title, trace.Session.Title)
	}
	if meta.EventCount != 1 || meta.UserTurns != 1 {
		t.Fatalf("trunk projection: events = %d, turns = %d", meta.EventCount, meta.UserTurns)
	}
}

func TestSessionTitleClearedByEmptySessionInfo(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t,
		header(root),
		`{"type":"message","id":"e1","parentId":null,"timestamp":"2026-07-21T14:34:22.963Z","message":{"role":"user","content":[{"type":"text","text":"rename the loader"}],"timestamp":1}}`,
		`{"type":"session_info","id":"e2","parentId":"e1","timestamp":"2026-07-21T14:34:23.000Z","name":"Named"}`,
		`{"type":"session_info","id":"e3","parentId":"e2","timestamp":"2026-07-21T14:34:24.000Z","name":""}`,
	)

	meta, err := (Adapter{}).Summarize(path)
	if err != nil {
		t.Fatal(err)
	}
	// An empty session_info name explicitly clears the title in pi; the
	// fallback chain then names the session from the first user message.
	if meta.Title != "rename the loader" {
		t.Fatalf("title = %q", meta.Title)
	}
	trace, err := (Adapter{}).Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if trace.Session.Title != meta.Title {
		t.Fatalf("trace title = %q, summary title = %q", trace.Session.Title, meta.Title)
	}
}

func TestReadSessionMatchesPiLineSkippingContract(t *testing.T) {
	root := t.TempDir()
	valid := header(root) + "\n" +
		`{"type":"message","id":"e1","parentId":null,"timestamp":"2026-07-21T14:34:22.963Z","message":{"role":"user","content":[{"type":"text","text":"hello"}],"timestamp":1}}`

	// pi skips blank and malformed lines without spending the header slot:
	// the first line that parses as JSON must be the header.
	accepted := map[string]string{
		"malformed prefix":  "not-json\n" + valid,
		"whitespace prefix": "   \n\t\n" + valid,
		"empty string id":   `{"type":"session","version":3,"id":"","timestamp":"2026-07-21T14:34:07.238Z","cwd":"/tmp"}`,
	}
	for name, content := range accepted {
		path := writeSession(t, content)
		if _, err := (Adapter{}).Parse(path); err != nil {
			t.Fatalf("%s: rejected: %v", name, err)
		}
		if _, err := (Adapter{}).Summarize(path); err != nil {
			t.Fatalf("%s: summarize rejected: %v", name, err)
		}
	}

	rejected := map[string]string{
		"entry before header": `{"type":"message","id":"u1","parentId":null,"timestamp":"2026-07-21T14:34:22.963Z","message":{"role":"user","content":"hi"}}` + "\n" + valid,
		"non-string id":       `{"type":"session","version":3,"id":123,"timestamp":"2026-07-21T14:34:07.238Z","cwd":"/tmp"}` + "\n" + valid,
		"null id":             `{"type":"session","version":3,"id":null,"timestamp":"2026-07-21T14:34:07.238Z","cwd":"/tmp"}`,
	}
	for name, content := range rejected {
		path := writeSession(t, content)
		if _, err := (Adapter{}).Parse(path); err == nil || !strings.Contains(err.Error(), "not a pi session") {
			t.Fatalf("%s: err = %v, want rejection", name, err)
		}
	}
}
