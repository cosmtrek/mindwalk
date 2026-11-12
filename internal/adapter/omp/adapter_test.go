package omp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/adapter/claudecode"
	"github.com/cosmtrek/mindwalk/internal/adapter/codex"
	"github.com/cosmtrek/mindwalk/internal/adapter/pi"
	"github.com/cosmtrek/mindwalk/internal/model"
)

func writeLines(t *testing.T, path string, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func titleSlot(title string) string {
	return mustJSON(map[string]any{"type": "title", "v": 1, "title": title, "source": "auto", "updatedAt": "2026-11-01T10:00:00.000Z", "pad": "      "})
}

func header(id, cwd, parentSession string) string {
	h := map[string]any{"type": "session", "version": 3, "id": id, "timestamp": "2026-11-01T10:00:00.000Z", "cwd": cwd}
	if parentSession != "" {
		h["parentSession"] = parentSession
	}
	return mustJSON(h)
}

// entry renders one tree entry; fields are merged over the tree linkage.
func entry(kind, id, parent string, fields map[string]any) string {
	e := map[string]any{"type": kind, "id": id, "parentId": nil, "timestamp": "2026-11-01T10:01:00.000Z"}
	if parent != "" {
		e["parentId"] = parent
	}
	for k, v := range fields {
		e[k] = v
	}
	return mustJSON(e)
}

func message(id, parent string, msg map[string]any) string {
	return entry("message", id, parent, map[string]any{"message": msg})
}

func toolCall(id, parent, callID, name string, args map[string]any) string {
	return message(id, parent, map[string]any{
		"role":     "assistant",
		"content":  []any{map[string]any{"type": "toolCall", "id": callID, "name": name, "arguments": args}},
		"provider": "anthropic",
		"model":    "claude-test",
	})
}

func toolResult(id, parent, callID, text string, isError bool) string {
	return message(id, parent, map[string]any{
		"role":       "toolResult",
		"toolCallId": callID,
		"content":    []any{map[string]any{"type": "text", "text": text}},
		"isError":    isError,
	})
}

func userMessage(id, parent, attribution, text string) string {
	return message(id, parent, map[string]any{
		"role":        "user",
		"content":     []any{map[string]any{"type": "text", "text": text}},
		"attribution": attribution,
	})
}

func targetOf(t *testing.T, event model.Event, path string) model.Target {
	t.Helper()
	for _, target := range event.Targets {
		if target.Path == path {
			return target
		}
	}
	t.Fatalf("event %d (%s) has no target %q: %#v", event.Seq, event.Tool, path, event.Targets)
	return model.Target{}
}

func TestParseReadsOmpToolDialect(t *testing.T) {
	root := t.TempDir()
	mainGo := filepath.Join(root, "internal", "app", "main.go")
	editDoc := "[" + mainGo + "#A1B2]\nPUT 3.=4:\n+fixed\n[internal/app/util.go:7#C3D4]\nPUT >1:\n+x\nMV internal/app/helpers.go"
	grepOut := strings.Join([]string{
		"# internal/",
		"## app/",
		"### main.go#A1B2",
		" 11:\tctx := nil",
		"*12:\tparse(ctx)",
		"...",
		"*40:\tparse(nil)",
		"[" + filepath.Join(root, "README.md") + "#9F00]",
		"*3:parse docs",
		"[Uneventful result elided]",
	}, "\n")
	globOut := "# internal/app/\nmain.go\nutil.go\n## testdata/\n# docs/\nguide.md"
	path := writeLines(t, filepath.Join(t.TempDir(), "2026-11-01T10-00-00-000Z_sess-1.jsonl"),
		titleSlot("Fix the parser"),
		header("sess-1", root, ""),
		entry("model_change", "m1", "", map[string]any{"model": "anthropic/claude-test"}),
		entry("model_change", "m2", "m1", map[string]any{"model": "anthropic/claude-small", "role": "smol"}),
		userMessage("u1", "m2", "user", "fix the parser"),
		toolCall("a1", "u1", "t1", "read", map[string]any{"path": "internal/app/main.go:10-20,30+5:raw"}),
		toolResult("r1", "a1", "t1", "package main", false),
		toolCall("a2", "r1", "t2", "read", map[string]any{"path": "skill://verify"}),
		toolResult("r2", "a2", "t2", "skill text", false),
		toolCall("a3", "r2", "t3", "grep", map[string]any{"pattern": "parse", "path": "internal;README.md"}),
		toolResult("r3", "a3", "t3", grepOut, false),
		toolCall("a4", "r3", "t4", "glob", map[string]any{"path": "internal/**/*.go"}),
		toolResult("r4", "a4", "t4", globOut, false),
		toolCall("a5", "r4", "t5", "edit", map[string]any{"input": editDoc}),
		toolResult("r5", "a5", "t5", "ok", true),
		toolCall("a6", "r5", "t6", "write", map[string]any{"path": "xd://lsp", "content": "{}"}),
		toolResult("r6", "a6", "t6", "ok", false),
		userMessage("u2", "r6", "agent", "[budget notice] wrap up"),
		userMessage("u3", "u2", "user", "also add a test"),
		toolCall("a7", "u3", "t7", "task", map[string]any{"tasks": []any{map[string]any{"name": "Tester", "task": "write tests"}}}),
	)

	trace, err := Adapter{}.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if s := trace.Session; s.Harness != "omp" || s.ID != "sess-1" || s.Cwd != root || s.Title != "Fix the parser" {
		t.Fatalf("session = %#v", s)
	}
	// a smol-role change does not switch the conversation model
	if trace.Session.Model != "anthropic/claude-test" {
		t.Fatalf("model = %q", trace.Session.Model)
	}
	if len(trace.Events) != 7 {
		t.Fatalf("events = %#v", trace.Events)
	}

	read := trace.Events[0]
	if read.Action != "read" || len(read.Targets) != 1 {
		t.Fatalf("read = %#v", read)
	}
	if got := targetOf(t, read, "internal/app/main.go"); !reflect.DeepEqual(got.Lines, [][2]int{{10, 20}, {30, 34}}) {
		t.Fatalf("read lines = %#v", got.Lines)
	}
	if uri := trace.Events[1]; uri.Action != "read" || len(uri.Targets) != 0 || len(uri.Outside) != 0 {
		t.Fatalf("internal URI read = %#v", uri)
	}

	grep := trace.Events[2]
	if grep.Action != "search" || len(grep.Targets) != 2 {
		t.Fatalf("grep = %#v", grep)
	}
	if got := targetOf(t, grep, "internal/app/main.go"); got.Touch != "hit" || got.Weak || !reflect.DeepEqual(got.Lines, [][2]int{{12, 12}, {40, 40}}) {
		t.Fatalf("grep main.go = %#v", got)
	}
	if got := targetOf(t, grep, "README.md"); !reflect.DeepEqual(got.Lines, [][2]int{{3, 3}}) {
		t.Fatalf("grep README = %#v", got)
	}

	glob := trace.Events[3]
	var globbed []string
	for _, target := range glob.Targets {
		globbed = append(globbed, target.Path)
	}
	if want := []string{"internal/app/main.go", "internal/app/util.go", "docs/guide.md"}; !reflect.DeepEqual(globbed, want) {
		t.Fatalf("glob targets = %v, want %v", globbed, want)
	}

	edit := trace.Events[4]
	if edit.Action != "edit" || !edit.IsError || len(edit.Targets) != 3 {
		t.Fatalf("edit = %#v", edit)
	}
	for _, p := range []string{"internal/app/main.go", "internal/app/util.go", "internal/app/helpers.go"} {
		if got := targetOf(t, edit, p); got.Touch != "edit" {
			t.Fatalf("edit target = %#v", got)
		}
	}
	if device := trace.Events[5]; device.Action != "other" || len(device.Targets) != 0 {
		t.Fatalf("xd:// write = %#v", device)
	}
	if pending := trace.Events[6]; pending.Tool != "task" || pending.OutcomeKnown {
		t.Fatalf("unanswered task = %#v", pending)
	}

	var notes, kinds []string
	for _, mark := range trace.Marks {
		kinds = append(kinds, mark.Type)
		if mark.Type == "user-message" {
			notes = append(notes, mark.Note)
		}
	}
	if !reflect.DeepEqual(notes, []string{"fix the parser", "also add a test"}) {
		t.Fatalf("user-message notes = %v", notes)
	}
	if !reflect.DeepEqual(kinds, []string{"user-message", "user-message", "subagent"}) {
		t.Fatalf("mark kinds = %v", kinds)
	}

	meta, err := Adapter{}.Summarize(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.EventCount != trace.Session.EventCount || meta.UserTurns != trace.Stats.UserTurns || meta.UserTurns != 2 {
		t.Fatalf("meta counts %d/%d, trace %d/%d", meta.EventCount, meta.UserTurns, trace.Session.EventCount, trace.Stats.UserTurns)
	}
	if meta.Title != trace.Session.Title || meta.Model != trace.Session.Model || meta.Auxiliary {
		t.Fatalf("meta = %#v", meta)
	}
}

func TestTitlePrecedence(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"slot wins", []string{titleSlot("Slot"), `{"type":"session","id":"s","cwd":"/r","title":"Header"}`, entry("title_change", "c1", "", map[string]any{"title": "Renamed"})}, "Slot"},
		{"rename over header", []string{titleSlot(""), `{"type":"session","id":"s","cwd":"/r","title":"Header"}`, entry("title_change", "c1", "", map[string]any{"title": "Renamed"})}, "Renamed"},
		{"header", []string{titleSlot(""), `{"type":"session","id":"s","cwd":"/r","title":"Header"}`}, "Header"},
		{"first user turn", []string{titleSlot(""), header("s", root, ""), userMessage("u1", "", "agent", "do the  assignment")}, "do the assignment"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeLines(t, filepath.Join(dir, fmt.Sprintf("s%d.jsonl", i)), tt.lines...)
			meta, err := Adapter{}.Summarize(path)
			if err != nil {
				t.Fatal(err)
			}
			if meta.Title != tt.want {
				t.Fatalf("title = %q, want %q", meta.Title, tt.want)
			}
		})
	}
}

func TestRecognitionStaysWithinSource(t *testing.T) {
	root := t.TempDir()
	store := t.TempDir()
	body := []string{header("legacy", root, ""), userMessage("u1", "", "user", "hi")}

	// a slot-less file has pi's shape: omp claims it only from its own store
	outside := writeLines(t, filepath.Join(t.TempDir(), "legacy.jsonl"), body...)
	if _, err := (Adapter{Dir: store}).Parse(outside); err == nil {
		t.Fatal("omp claimed a slot-less file outside its store")
	}
	inside := writeLines(t, filepath.Join(store, "-r", "legacy.jsonl"), body...)
	if trace, err := (Adapter{Dir: store}).Parse(inside); err != nil || trace.Session.ID != "legacy" {
		t.Fatalf("legacy store file: trace = %#v, err = %v", trace, err)
	}

	slotted := writeLines(t, filepath.Join(t.TempDir(), "slotted.jsonl"), append([]string{titleSlot("T")}, body...)...)
	if _, err := (Adapter{Dir: store}).Parse(slotted); err != nil {
		t.Fatalf("slotted file outside store: %v", err)
	}
	if _, err := (pi.Adapter{}).Parse(slotted); err == nil {
		t.Fatal("pi claimed an omp session")
	}
	if _, err := (claudecode.Adapter{}).Parse(slotted); err == nil {
		t.Fatal("claude code claimed an omp session")
	}
	if _, err := (codex.Adapter{}).Parse(slotted); err == nil {
		t.Fatal("codex claimed an omp session")
	}
}

func TestParseFollowsActiveLeaf(t *testing.T) {
	root := t.TempDir()
	lines := []string{
		titleSlot("T"),
		header("s", root, ""),
		userMessage("u1", "", "user", "start"),
		toolCall("a1", "u1", "abandoned", "bash", map[string]any{"command": "echo abandoned"}),
		toolResult("r1", "a1", "abandoned", "abandoned", false),
		// the user branched back to u1
		toolCall("a2", "u1", "kept", "bash", map[string]any{"command": "echo kept"}),
		toolResult("r2", "a2", "kept", "kept", false),
	}
	commands := func(lines ...string) []string {
		path := writeLines(t, filepath.Join(t.TempDir(), "s.jsonl"), lines...)
		trace, err := Adapter{}.Parse(path)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, event := range trace.Events {
			out = append(out, event.Summary)
		}
		return out
	}
	if got := commands(lines...); len(got) != 1 || !strings.HasPrefix(got[0], "echo kept") {
		t.Fatalf("trunk = %v", got)
	}
	// late usage restores the leaf it captured instead of becoming the leaf
	late := entry("model_usage", "mu", "r2", map[string]any{"activeLeafId": "r1"})
	if got := commands(append(lines, late)...); len(got) != 1 || !strings.HasPrefix(got[0], "echo abandoned") {
		t.Fatalf("late-usage trunk = %v", got)
	}
	cleared := entry("model_usage", "mu", "r2", map[string]any{"activeLeafId": nil})
	if got := commands(append(lines, cleared)...); len(got) != 0 {
		t.Fatalf("cleared trunk = %v", got)
	}
}

func TestSummarizeMarksOnlyNestedSubagentsAuxiliary(t *testing.T) {
	root := t.TempDir()
	store := t.TempDir()
	parent := filepath.Join(store, "-r", "2026-11-01T10-00-00-000Z_root.jsonl")
	writeLines(t, parent, titleSlot("Root"), header("root", root, ""))
	child := writeLines(t, filepath.Join(strings.TrimSuffix(parent, ".jsonl"), "Scout.jsonl"),
		titleSlot(""),
		header("child", root, parent),
		entry("session_init", "i1", "", map[string]any{"agent": "scout", "task": "look"}),
	)
	// a fork records parentSession too but lives beside its source
	fork := writeLines(t, filepath.Join(store, "-r", "2026-11-01T11-00-00-000Z_fork.jsonl"),
		titleSlot("Fork"),
		header("fork", root, parent),
	)

	a := Adapter{Dir: store}
	meta, err := a.Summarize(child)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Auxiliary || meta.Agent == nil || meta.Agent.SourceID != "Scout" || meta.Agent.Role != "scout" {
		t.Fatalf("child meta = %#v agent = %#v", meta, meta.Agent)
	}
	if forkMeta, err := a.Summarize(fork); err != nil || forkMeta.Auxiliary {
		t.Fatalf("fork meta = %#v, err = %v", forkMeta, err)
	}
	sessions, err := a.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("listed = %#v", sessions)
	}
}
