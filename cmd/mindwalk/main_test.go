package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestParseOutputArgsAllowsTrailingOutput(t *testing.T) {
	positional, out, err := parseOutputArgs([]string{"repo", "-o", "citymap.json"})
	if err != nil {
		t.Fatal(err)
	}
	if len(positional) != 1 || positional[0] != "repo" {
		t.Fatalf("positional = %#v", positional)
	}
	if out != "citymap.json" {
		t.Fatalf("out = %q", out)
	}
}

func TestHealthCommandWritesTextForClaudeSession(t *testing.T) {
	session := writeHealthFixture(t, filepath.Join(t.TempDir(), "claude.jsonl"),
		`{"type":"user","timestamp":"2026-07-21T00:00:00Z","sessionId":"claude-root","cwd":"/tmp","message":{"role":"user","content":"inspect"}}`,
	)

	stdout, err := captureHealthStdout(t, func() error {
		return healthCommand([]string{session})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout, "Session health\n") || !strings.Contains(stdout, "File reads:") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestHealthCommandWritesCleanJSONForCodexWithFlagInEitherPosition(t *testing.T) {
	session := writeHealthFixture(t, filepath.Join(t.TempDir(), "codex.jsonl"),
		`{"timestamp":"2026-07-21T00:00:00Z","type":"session_meta","payload":{"id":"codex-root","cwd":"/tmp"}}`,
	)

	for _, args := range [][]string{{"--json", session}, {session, "--json"}} {
		stdout, err := captureHealthStdout(t, func() error {
			return healthCommand(args)
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(stdout, "{") {
			t.Fatalf("stdout does not start with JSON object: %q", stdout)
		}
		if strings.Contains(stdout, "mindwalk:") || strings.Contains(stdout, "judging") {
			t.Fatalf("stdout contains non-JSON status text: %q", stdout)
		}
		var summary model.SessionHealth
		if err := json.Unmarshal([]byte(stdout), &summary); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
		}
		if summary.Version != model.SessionHealthVersion || len(summary.Signals.Reads.Affects) == 0 {
			t.Fatalf("summary = %#v", summary)
		}
	}
}

func TestHealthCommandExternalStandaloneSessionLeavesAgentContextUnavailable(t *testing.T) {
	session := writeHealthFixture(t, filepath.Join(t.TempDir(), "standalone.jsonl"),
		`{"type":"assistant","timestamp":"2026-07-21T00:00:00Z","sessionId":"standalone-root","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-child","name":"Task","input":{"prompt":"inspect"}}]}}`,
	)

	stdout, err := captureHealthStdout(t, func() error {
		return healthCommand([]string{session, "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var summary model.SessionHealth
	if err := json.Unmarshal([]byte(stdout), &summary); err != nil {
		t.Fatal(err)
	}
	if got := summary.Signals.Subagents.Reason; got != model.HealthReasonAgentContextMissing {
		t.Fatalf("subagent reason = %q, want %q", got, model.HealthReasonAgentContextMissing)
	}
	if summary.Signals.Subagents.MissingTraceCount != 0 {
		t.Fatalf("standalone session pretended child traces were missing: %#v", summary.Signals.Subagents)
	}
}

func TestHealthCommandDefaultSessionDirectoryIncludesAuxiliaryClaudeSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir := filepath.Join(home, ".claude", "projects", "demo")
	root := writeHealthFixture(t, filepath.Join(projectDir, "root-id.jsonl"),
		`{"type":"assistant","timestamp":"2026-07-21T00:00:00Z","sessionId":"root-id","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-child","name":"Task","input":{"description":"Child","subagent_type":"Explore","prompt":"inspect"}}]}}`,
	)
	childDir := filepath.Join(projectDir, "root-id", "subagents")
	writeHealthFixture(t, filepath.Join(childDir, "agent-child.jsonl"),
		`{"type":"user","timestamp":"2026-07-21T00:00:01Z","sessionId":"root-id","agentId":"child-id","isSidechain":true,"message":{"role":"user","content":"internal"}}`,
	)
	writeHealthFixture(t, filepath.Join(childDir, "agent-child.meta.json"),
		`{"agentType":"Explore","description":"Child","toolUseId":"call-child","spawnDepth":1}`,
	)

	stdout, err := captureHealthStdout(t, func() error {
		return healthCommand([]string{root, "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var summary model.SessionHealth
	if err := json.Unmarshal([]byte(stdout), &summary); err != nil {
		t.Fatal(err)
	}
	if got := summary.Signals.Subagents.ExactCount; got != 1 {
		t.Fatalf("exact subagent traces = %d, want 1; signal = %#v", got, summary.Signals.Subagents)
	}
	if summary.Signals.Subagents.MissingTraceCount != 0 || summary.Signals.Subagents.Reason != model.HealthReasonExactAgentLinks {
		t.Fatalf("subagent signal = %#v", summary.Signals.Subagents)
	}
}

func TestHealthCommandRejectsMissingSession(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	if err := healthCommand([]string{missing}); err == nil {
		t.Fatal("healthCommand returned nil for a missing session")
	}
}

func TestHealthCommandRequiresExactlyOneSession(t *testing.T) {
	for _, args := range [][]string{nil, {"one.jsonl", "two.jsonl"}} {
		err := healthCommand(args)
		if err == nil || err.Error() != "usage: mindwalk health <session.jsonl> [--json]" {
			t.Fatalf("healthCommand(%v) error = %v", args, err)
		}
	}
}

func writeHealthFixture(t *testing.T, path string, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func captureHealthStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	t.Cleanup(func() { os.Stdout = original })

	fnErr := fn()
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = original
	data, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	return string(data), fnErr
}
