package judge

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseClaudeEnvelopePicksMainModel(t *testing.T) {
	raw := `{"type":"result","result":"{\"ok\":true}","modelUsage":{
		"claude-haiku-4-5":{"inputTokens":523,"outputTokens":13},
		"claude-sonnet-5":{"inputTokens":1,"cacheReadInputTokens":3289,"cacheCreationInputTokens":5563}}}`
	got := parseClaudeEnvelope(raw)
	if got.Text != `{"ok":true}` {
		t.Fatalf("text = %q", got.Text)
	}
	// The helper model wrote more output tokens, but only the judge read the
	// full evidence document — input volume picks the right one.
	if got.Model != "claude-sonnet-5" {
		t.Fatalf("model = %q, want claude-sonnet-5", got.Model)
	}
}

func TestParseClaudeEnvelopeFallsBackToRawText(t *testing.T) {
	raw := `plain text, not an envelope {"ok":true}`
	got := parseClaudeEnvelope(raw)
	if got.Text != raw || got.Model != "" {
		t.Fatalf("fallback = %#v", got)
	}
}

func TestCodexModelReadsPreamble(t *testing.T) {
	raw := "OpenAI Codex v0.143.0\n--------\nworkdir: /tmp\nmodel: gpt-5.6-sol\nprovider: openai\n--------\n{\"ok\":true}\n"
	if got := codexModel(raw); got != "gpt-5.6-sol" {
		t.Fatalf("model = %q", got)
	}
	if got := codexModel("no preamble at all"); got != "" {
		t.Fatalf("expected empty model, got %q", got)
	}
}

func TestCodexExecArgsExcludeRemovedFeatures(t *testing.T) {
	args := codexExecArgs("/tmp/judge")
	if slices.Contains(args, "browser_use_full_cdp_access") {
		t.Fatal("codex args include removed browser_use_full_cdp_access feature")
	}
}

func TestTruncateFailureDetailPreservesUTF8(t *testing.T) {
	detail := strings.Repeat("a", 499) + "界tail"
	got := truncateFailureDetail(detail)

	if !utf8.ValidString(got) {
		t.Fatalf("truncated detail is not valid UTF-8: %q", got)
	}
	want := strings.Repeat("a", 499) + "界"
	if got != want {
		t.Fatalf("truncated detail = %q, want %q", got, want)
	}
}

func TestTruncateFailureDetailRepairsInvalidUTF8(t *testing.T) {
	got := truncateFailureDetail(string([]byte{'o', 'k', 0xff}))
	if !utf8.ValidString(got) {
		t.Fatalf("failure detail contains invalid UTF-8: %q", got)
	}
}

func TestCrushModelReadsVerboseLog(t *testing.T) {
	raw := "INFO Running in non-interactive mode\n" +
		"INFO Created session for non-interactive run session_id=abc\n" +
		"INFO ModelProvider called provider=zai model=glm-5.2\n" +
		"INFO Skill turn summary component=skills\n"
	if got := crushModel(raw); got != "glm-5.2" {
		t.Fatalf("model = %q, want glm-5.2", got)
	}

	if got := crushModel("no model provider line at all"); got != "" {
		t.Fatalf("expected empty model, got %q", got)
	}
}
