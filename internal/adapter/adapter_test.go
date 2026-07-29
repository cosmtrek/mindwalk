package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestAgentNodeIDIsStableAndRootScoped(t *testing.T) {
	got := AgentNodeID("codex", "root-key", "codex-agent:child")
	if got != AgentNodeID("codex", "root-key", "codex-agent:child") {
		t.Fatal("unstable")
	}
	if !regexp.MustCompile(`^agt_[0-9a-f]{24}$`).MatchString(got) {
		t.Fatalf("id=%q", got)
	}
	if got == AgentNodeID("codex", "other-root", "codex-agent:child") {
		t.Fatal("not root scoped")
	}
	if got == AgentNodeID("claude-code", "root-key", "codex-agent:child") {
		t.Fatal("not harness scoped")
	}
}

func TestAgentInstructionPreviewNormalizesAndTruncatesRunes(t *testing.T) {
	if got := AgentInstructionPreview("  review\n\tthis   change  "); got != "review this change" {
		t.Fatalf("normalized preview = %q", got)
	}

	exact := strings.Repeat("字", 240)
	if got := AgentInstructionPreview(exact); got != exact {
		t.Fatalf("preview at limit changed: %q", got)
	}

	got := AgentInstructionPreview(strings.Repeat("字", 241))
	if len([]rune(got)) != 240 {
		t.Fatalf("truncated preview is %d runes, want 240", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated preview missing ellipsis: %q", got)
	}
}

func TestSessionKeyIsStableAndSourceSpecific(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	first := SessionKey("codex", path)
	if second := SessionKey("codex", path); second != first {
		t.Fatalf("SessionKey changed: %q != %q", first, second)
	}
	if other := SessionKey("claude-code", path); other == first {
		t.Fatalf("SessionKey ignored harness: %q", other)
	}
	if other := SessionKey("codex", path+".copy"); other == first {
		t.Fatalf("SessionKey ignored path: %q", other)
	}
}

func TestUserMessageNoteStaysWithinRuneBudget(t *testing.T) {
	long := strings.Repeat("字", userMessageNoteLimit+50)
	note := UserMessageNote(long)
	if got := len([]rune(note)); got != userMessageNoteLimit {
		t.Fatalf("truncated note is %d runes, want %d (ellipsis must fit the budget)", got, userMessageNoteLimit)
	}
	if !strings.HasSuffix(note, "…") {
		t.Fatalf("truncated note missing ellipsis marker: %q", note[len(note)-12:])
	}
	exact := strings.Repeat("a", userMessageNoteLimit)
	if UserMessageNote(exact) != exact {
		t.Fatal("text at the limit must pass through untouched")
	}
}

func TestSummarizeToolTruncatesCommandAtRuneBoundary(t *testing.T) {
	command := strings.Repeat("a", 92) + "界tail"
	summary := SummarizeTool("exec", map[string]any{"cmd": command}, nil, nil, false)

	if !utf8.ValidString(summary) {
		t.Fatalf("summary contains invalid UTF-8: %q", summary)
	}
	want := strings.Repeat("a", 92) + "界... -> 0 targets, 0 outside"
	if summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
}

func TestSummarizeToolExtractsCommandsFromExecWrapper(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "single command",
			source: `const r = await tools.exec_command({cmd:"jq '.city | keys' snapshot.json",workdir:"/tmp"}); text(r.output)`,
			want:   "jq '.city | keys' snapshot.json -> 0 targets, 0 outside",
		},
		{
			name:   "multiple tool calls",
			source: `const rs = await Promise.all([tools.exec_command({cmd:"go test ./..."}), tools.exec_command({cmd:"go vet ./..."})]); text(rs)`,
			want:   "go test ./... (+1 more tool call) -> 0 targets, 0 outside",
		},
		{
			name:   "dynamic command keeps nested tool identity",
			source: `const r = await tools.exec_command({cmd,workdir:"/tmp"}); text(r.output)`,
			want:   "exec_command -> 0 targets, 0 outside",
		},
		{
			name:   "non-command tool keeps nested identity",
			source: `const p = await tools.update_plan({plan:[]}); text(p)`,
			want:   "update_plan -> 0 targets, 0 outside",
		},
		{
			name:   "mixed calls retain count",
			source: `const r = await tools.exec_command({cmd:"go test ./..."}); const p = await tools.update_plan({plan:[]}); text(r.output); text(p)`,
			want:   "go test ./... (+1 more tool call) -> 0 targets, 0 outside",
		},
		{
			name:   "plain orchestration remains generic",
			source: `text("done")`,
			want:   "exec -> 0 targets, 0 outside",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SummarizeTool("exec", map[string]any{"_raw": tt.source}, nil, nil, false)
			if got != tt.want {
				t.Fatalf("summary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInjectedUserMessageShape(t *testing.T) {
	injected := []string{
		"<system-reminder>context</system-reminder>",
		"<command-name>/review</command-name>",
		"<local-command-caveat>Caveat: …</local-command-caveat>",
		"<environment_context>\nshell: zsh\n</environment_context>",
		"<turn_aborted>true</turn_aborted>",
		"# AGENTS.md instructions for /repo\n\nrules",
	}
	for _, text := range injected {
		if !InjectedUserMessage(text) {
			t.Fatalf("should be injected: %q", text)
		}
	}
	// Real tasks that merely start with markup must survive.
	genuine := []string{
		"fix the login bug",
		"<div class=\"card\"> 这个组件为什么不居中",
		"<Button onClick={…}/> renders twice, why",
		"# AGENTS 文件怎么写",
	}
	for _, text := range genuine {
		if InjectedUserMessage(text) {
			t.Fatalf("real task misclassified as injected: %q", text)
		}
	}
}

func TestBuildEventKeepsExecAggregatedAndFindsSingleCommandTarget(t *testing.T) {
	root := t.TempDir()
	writeAdapterTestFile(t, root, "README.md")
	source := `const r = await tools.exec_command({cmd:"sed -n '1,20p' README.md",workdir:` + jsonString(t, root) + `});`

	event := buildExecEvent(root, map[string]any{"_raw": source})
	if event.Tool != "exec" || event.Action != "read" {
		t.Fatalf("event = %#v", event)
	}
	if len(event.Targets) != 1 || event.Targets[0].Path != "README.md" || !event.Targets[0].Weak || event.Targets[0].Touch != "read" {
		t.Fatalf("targets = %#v", event.Targets)
	}
}

func TestBuildEventExtractsApplyPatchFromExec(t *testing.T) {
	root := t.TempDir()
	writeAdapterTestFile(t, root, "src/main.go")
	patch := "*** Begin Patch\n*** Update File: src/main.go\n@@\n-old\n+new\n*** End Patch"
	source := `const patch = ` + jsonString(t, patch) + `; text(await tools.apply_patch(patch));`

	event := buildExecEvent(root, map[string]any{"_raw": source})
	if event.Tool != "exec" || event.Action != "edit" {
		t.Fatalf("event = %#v", event)
	}
	if len(event.Targets) != 1 || event.Targets[0].Path != "src/main.go" || event.Targets[0].Touch != "edit" || event.Targets[0].Weak {
		t.Fatalf("targets = %#v", event.Targets)
	}
}

func TestBuildEventExtractsPromiseAllCommandTargets(t *testing.T) {
	root := t.TempDir()
	writeAdapterTestFile(t, root, "first/main.go")
	writeAdapterTestFile(t, root, "second/main.go")
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	source := `const rs = await Promise.all([
  tools.exec_command({cmd:"sed -n '1,20p' main.go",workdir:` + jsonString(t, first) + `}),
  tools.exec_command({"cmd":"rg TODO main.go","workdir":` + jsonString(t, second) + `})
]);`

	event := buildExecEvent(root, map[string]any{"code": source})
	if event.Tool != "exec" || event.Action != "exec" {
		t.Fatalf("event = %#v", event)
	}
	if len(event.Targets) != 2 {
		t.Fatalf("targets = %#v", event.Targets)
	}
	want := []struct {
		path  string
		touch string
	}{{"first/main.go", "read"}, {"second/main.go", "hit"}}
	for i, target := range event.Targets {
		if target.Path != want[i].path || target.Touch != want[i].touch || !target.Weak {
			t.Fatalf("target %d = %#v", i, target)
		}
	}
}

func TestBuildEventDecodesEscapedExecStrings(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, `quoted"dir`)
	writeAdapterTestFile(t, workdir, "src/main.go")
	command := `sed -n "1,20p" src/main.go`
	source := `tools.exec_command({cmd:` + jsonString(t, command) + `,workdir:` + jsonString(t, workdir) + `});`

	event := buildExecEvent(root, map[string]any{"script": source})
	if len(event.Targets) != 1 || event.Targets[0].Path != `quoted"dir/src/main.go` || !event.Targets[0].Weak {
		t.Fatalf("targets = %#v", event.Targets)
	}
}

func TestBuildEventAcceptsWorkdirBeforeCommand(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "sub")
	writeAdapterTestFile(t, workdir, "main.go")
	source := `tools.exec_command({workdir:` + jsonString(t, workdir) + `,cmd:"sed main.go"});`

	event := buildExecEvent(root, map[string]any{"_raw": source})
	if len(event.Targets) != 1 || event.Targets[0].Path != "sub/main.go" {
		t.Fatalf("targets = %#v", event.Targets)
	}
}

func TestBuildEventExecActionRequiresEveryCommandToVerify(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "all verification commands",
			source: `Promise.all([tools.exec_command({cmd:"go test ./..."}), tools.exec_command({cmd:"make test"})])`,
			want:   "verify",
		},
		{
			name:   "verification and ordinary command",
			source: `Promise.all([tools.exec_command({cmd:"go test ./..."}), tools.exec_command({cmd:"sed -n '1,20p' README.md"})])`,
			want:   "exec",
		},
		{
			name:   "verification and another tool",
			source: `Promise.all([tools.exec_command({cmd:"go test ./..."}), tools.apply_patch("*** Begin Patch")])`,
			want:   "exec",
		},
		{
			name:   "dynamic command",
			source: `tools.exec_command({cmd,workdir:"/tmp"})`,
			want:   "exec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := buildExecEvent(t.TempDir(), map[string]any{"_raw": tt.source})
			if event.Tool != "exec" || event.Action != tt.want {
				t.Fatalf("event = %#v, want action %q", event, tt.want)
			}
		})
	}
}

func TestBuildEventClassifiesBashSearchCommands(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{`grep -rn "Pair with AI" src --include="*.ts" | head -5`, "search"},
		{`find . -name "*.go" | wc -l`, "search"},
		{`cd /repo && rg TODO internal`, "search"},
		{`git grep -n hook`, "search"},
		{`FOO=1 grep -c x file.txt 2>/dev/null`, "search"},
		{`ls web/src`, "search"},
		{`grep x file > out.txt`, "exec"},
		{`find . -name "*.tmp" -delete`, "exec"},
		{`grep x file && rm file`, "exec"},
		{`npm install 2>&1 | tail -3`, "exec"},
		{`echo done`, "exec"},
		{`go test ./... | tail -5`, "verify"},
		{`cat internal/model/stats.go`, "read"},
		{`sed -n '1,240p' web/src/ui/Hud.tsx`, "read"},
		{`nl -ba main.go | head -50`, "read"},
		{`head -n 20 Makefile`, "read"},
		{`sed -i '' 's/a/b/g' main.go`, "exec"},
		{`cat notes.md > backup.md`, "exec"},
		{`cat main.go && rm main.go`, "exec"},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			trace := &model.Trace{Session: model.TraceSession{Cwd: t.TempDir()}}
			event := BuildEvent(trace, ToolCall{Name: "Bash", Input: map[string]any{"command": tt.command}}, ToolResult{})
			if event.Action != tt.want {
				t.Fatalf("action(%q) = %q, want %q", tt.command, event.Action, tt.want)
			}
		})
	}
}

func TestBuildEventPreservesOutcomeCertainty(t *testing.T) {
	trace := &model.Trace{Session: model.TraceSession{Cwd: t.TempDir()}}
	call := ToolCall{Name: "Bash", Input: map[string]any{"command": "go test ./..."}}

	known := BuildEvent(trace, call, ToolResult{OutcomeKnown: true})
	if !known.OutcomeKnown || known.IsError {
		t.Fatalf("known event = %#v", known)
	}

	unknown := BuildEvent(trace, call, ToolResult{})
	if unknown.OutcomeKnown || unknown.IsError {
		t.Fatalf("unknown event = %#v", unknown)
	}
}

func TestBuildEventExecActionAllSearchCommands(t *testing.T) {
	source := `Promise.all([tools.exec_command({cmd:"rg TODO main.go"}), tools.exec_command({cmd:"ls src"})])`
	event := buildExecEvent(t.TempDir(), map[string]any{"_raw": source})
	if event.Tool != "exec" || event.Action != "search" {
		t.Fatalf("event = %#v, want action %q", event, "search")
	}
}

func TestBuildEventExecActionAllReadCommands(t *testing.T) {
	source := `Promise.all([tools.exec_command({cmd:"sed -n '1,20p' main.go"}), tools.exec_command({cmd:"cat README.md"})])`
	event := buildExecEvent(t.TempDir(), map[string]any{"_raw": source})
	if event.Tool != "exec" || event.Action != "read" {
		t.Fatalf("event = %#v, want action %q", event, "read")
	}
}

func TestCommandReadPaths(t *testing.T) {
	tests := []struct {
		command string
		want    []string
	}{
		{`sed -n '1,240p' internal/adapter/adapter.go`, []string{"internal/adapter/adapter.go"}},
		{`cat a.go b.go`, []string{"a.go", "b.go"}},
		{`head -n 20 Makefile`, []string{"Makefile"}},
		{`tail -f logs/app.log`, []string{"logs/app.log"}},
		{`cat src/main.go | rg TODO`, []string{"src/main.go"}},
		{`sed -i '' 's/x/y/' a.go`, nil},
		{`cat file.go > copy.go`, nil},
		{`grep -rn TODO src`, nil},
		{`cat *.go`, nil},
		{`cat <<EOF > notes.md`, nil},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := commandReadPaths(tt.command)
			if len(got) != len(tt.want) {
				t.Fatalf("commandReadPaths(%q) = %#v, want %#v", tt.command, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("commandReadPaths(%q) = %#v, want %#v", tt.command, got, tt.want)
				}
			}
		})
	}
}

func TestBuildEventDoesNotPairDistantExecWorkdir(t *testing.T) {
	root := t.TempDir()
	writeAdapterTestFile(t, root, "README.md")
	source := `tools.exec_command({cmd:"sed README.md"}); const metadata = {workdir:"/tmp/not-the-command-workdir"};`

	event := buildExecEvent(root, map[string]any{"_raw": source})
	if len(event.Targets) != 1 || event.Targets[0].Path != "README.md" || !event.Targets[0].Weak {
		t.Fatalf("targets = %#v", event.Targets)
	}
	if len(event.Outside) != 0 {
		t.Fatalf("outside = %#v", event.Outside)
	}
}

func TestBuildEventIgnoresExecExamplesInStringsAndComments(t *testing.T) {
	root := t.TempDir()
	writeAdapterTestFile(t, root, "README.md")
	source := "const quoted = 'tools.exec_command({cmd:\"go test ./...\"})';\n" +
		"const template = `tools.exec_command({cmd:\"sed README.md\"})`;\n" +
		"// tools.exec_command({cmd:\"sed README.md\"})\n" +
		"/* tools.exec_command({cmd:\"sed README.md\"}) */\n" +
		"text(quoted + template);"

	event := buildExecEvent(root, map[string]any{"_raw": source})
	if event.Action != "exec" || len(event.Targets) != 0 {
		t.Fatalf("event = %#v", event)
	}
	if event.Summary != "exec -> 0 targets, 0 outside" {
		t.Fatalf("summary = %q", event.Summary)
	}
}

func TestBuildEventExtractsJSReplTargets(t *testing.T) {
	root := t.TempDir()
	writeAdapterTestFile(t, root, "packages/db/src/index.ts")
	code := `const db = await import("./packages/db/src/index.ts")`

	for _, key := range []string{"code", "_raw"} {
		t.Run(key, func(t *testing.T) {
			trace := &model.Trace{Session: model.TraceSession{Cwd: root}}
			event := BuildEvent(trace, ToolCall{Name: "js_repl", Input: map[string]any{key: code}}, ToolResult{})
			if event.Tool != "js_repl" || event.Action != "exec" {
				t.Fatalf("event = %#v", event)
			}
			if len(event.Targets) != 1 || event.Targets[0].Path != "packages/db/src/index.ts" || !event.Targets[0].Weak {
				t.Fatalf("targets = %#v", event.Targets)
			}
		})
	}
}

func buildExecEvent(cwd string, input map[string]any) model.Event {
	trace := &model.Trace{Session: model.TraceSession{Cwd: cwd}}
	return BuildEvent(trace, ToolCall{Name: "exec", Input: input}, ToolResult{})
}

func writeAdapterTestFile(t *testing.T, root, path string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestBuildEventClassifiesPiCoreTools(t *testing.T) {
	root := t.TempDir()
	writeAdapterTestFile(t, root, "src/main.go")
	abs := filepath.Join(root, "src", "main.go")

	tests := []struct {
		tool   string
		input  map[string]any
		action string
		path   string
		touch  string
		weak   bool
	}{
		{"read", map[string]any{"path": abs, "offset": float64(10), "limit": float64(5)}, "read", "src/main.go", "read", false},
		{"edit", map[string]any{"path": abs, "edits": []any{map[string]any{"oldText": "a", "newText": "b"}}}, "edit", "src/main.go", "edit", false},
		{"write", map[string]any{"path": abs, "content": "x"}, "edit", "src/main.go", "edit", false},
		{"grep", map[string]any{"pattern": "TODO", "path": abs}, "search", "src/main.go", "hit", true},
		{"find", map[string]any{"pattern": "*.go", "path": abs}, "search", "src/main.go", "hit", true},
		{"ls", map[string]any{"path": abs}, "search", "src/main.go", "hit", true},
		{"bash", map[string]any{"command": "go test ./... | tail -5"}, "verify", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			trace := &model.Trace{Session: model.TraceSession{Cwd: root}}
			event := BuildEvent(trace, ToolCall{Name: tt.tool, Input: tt.input}, ToolResult{})
			if event.Action != tt.action {
				t.Fatalf("action = %q, want %q", event.Action, tt.action)
			}
			if tt.path == "" {
				return
			}
			if len(event.Targets) != 1 {
				t.Fatalf("targets = %#v", event.Targets)
			}
			target := event.Targets[0]
			if target.Path != tt.path || target.Touch != tt.touch || target.Weak != tt.weak {
				t.Fatalf("target = %#v", target)
			}
		})
	}
}

func TestBuildEventPiReadRecordsLineRange(t *testing.T) {
	trace := &model.Trace{Session: model.TraceSession{Cwd: t.TempDir()}}
	input := map[string]any{"path": "/abs/elsewhere.go", "offset": float64(10), "limit": float64(5)}
	event := BuildEvent(trace, ToolCall{Name: "read", Input: input}, ToolResult{})
	if event.Action != "read" {
		t.Fatalf("action = %q", event.Action)
	}
	if len(event.Outside) != 1 {
		t.Fatalf("outside = %#v", event.Outside)
	}
}
