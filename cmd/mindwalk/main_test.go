package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestParseTraceRedactsBeforeCLIExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	contents := strings.Join([]string{
		`{"type":"ai-title","timestamp":"2026-07-13T00:00:00Z","sessionId":"fixture","cwd":"/tmp","aiTitle":"token=synthetic-export-secret"}`,
		`{"type":"user","timestamp":"2026-07-13T00:00:01Z","sessionId":"fixture","cwd":"/tmp","message":{"role":"user","content":"inspect"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	trace, err := parseTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(trace.Session.Title, "synthetic-export-secret") || !strings.Contains(trace.Session.Title, "[REDACTED]") {
		t.Fatalf("export title = %q", trace.Session.Title)
	}
}
