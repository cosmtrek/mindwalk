package crush

import (
	"encoding/json"
	"testing"
)

// TestDecodePartsReasoning ensures reasoning parts decode without
// surfacing in the message text but still validate their payload.
// Reasoning is captured by Crush for every assistant turn; the
// visualizer does not have a thinking lane today, but the schema
// coverage keeps a future bump loud.
func TestDecodePartsReasoning(t *testing.T) {
	raw := writeParts(
		t,
		map[string]any{
			"type": "reasoning",
			"data": map[string]any{"thinking": "the user asked for X, I should do Y first"},
		},
		map[string]any{"type": "text", "data": map[string]any{"text": "Doing the thing."}},
	)

	result, err := decodeParts(raw, "")
	if err != nil {
		t.Fatal(err)
	}

	if result.text != "Doing the thing." {
		t.Fatalf("text = %q", result.text)
	}

	if len(result.events) != 0 {
		t.Fatalf("reasoning should not emit events, got %+v", result.events)
	}
}

// TestDecodePartsFinishStopSetsUserFinish verifies the finish/stop
// branch records userFinish=true so the Parse loop emits a
// user-message mark for the trace.
func TestDecodePartsFinishStopSetsUserFinish(t *testing.T) {
	raw := writeParts(t,
		map[string]any{"type": "text", "data": map[string]any{"text": "ship the fix"}},
		map[string]any{"type": "finish", "data": map[string]any{"reason": "stop", "time": 0}},
	)

	result, err := decodeParts(raw, "")
	if err != nil {
		t.Fatal(err)
	}

	if !result.userFinish {
		t.Fatalf("userFinish = false, want true for finish.reason=stop")
	}

	if result.text != "ship the fix" {
		t.Fatalf("text = %q", result.text)
	}
}

// TestDecodePartsFinishOtherReasonsIgnored verifies only finish/stop
// triggers the user-message mark — other reasons (length, tool_use,
// safety) do not.
func TestDecodePartsFinishOtherReasonsIgnored(t *testing.T) {
	for _, reason := range []string{"length", "tool_use", "safety", "unknown_future_reason"} {
		t.Run(reason, func(t *testing.T) {
			raw := writeParts(t,
				map[string]any{"type": "text", "data": map[string]any{"text": "irrelevant"}},
				map[string]any{"type": "finish", "data": map[string]any{"reason": reason}},
			)

			result, err := decodeParts(raw, "")
			if err != nil {
				t.Fatal(err)
			}

			if result.userFinish {
				t.Fatalf("userFinish = true for reason %q, want false", reason)
			}
		})
	}
}

// TestDecodePartsShellCommandEmitsExecEvent verifies the parser
// converts a shell_command part into a bash exec event with the
// command, output, and exit code. This surfaces bang-mode commands
// in the timeline.
func TestDecodePartsShellCommandEmitsExecEvent(t *testing.T) {
	raw := writeParts(t,
		map[string]any{"type": "shell_command", "data": map[string]any{
			"command":   "ls -la",
			"output":    "total 0\n",
			"exit_code": 0,
		}},
	)

	result, err := decodeParts(raw, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.events) != 1 {
		t.Fatalf("events = %d, want 1 (shell_command → bash exec)", len(result.events))
	}

	if result.events[0].Name != "bash" {
		t.Fatalf("tool name = %q, want bash", result.events[0].Name)
	}

	if result.events[0].Input["command"] != "ls -la" {
		t.Fatalf("command = %v", result.events[0].Input["command"])
	}

	if len(result.results) != 1 || result.results[0].Content != "total 0\n" {
		t.Fatalf("results = %+v", result.results)
	}

	if result.results[0].IsError {
		t.Fatalf("exit code 0 should not be an error")
	}
}

// TestDecodePartsShellCommandDeduplicatedWithBash verifies that when
// a message has both a bash tool call and a shell_command part for
// the same command, only one event is emitted (the tool call wins).
func TestDecodePartsShellCommandDeduplicatedWithBash(t *testing.T) {
	raw := writeParts(t,
		map[string]any{"type": "tool_call", "data": map[string]any{
			"id": "call-1", "name": "bash", "input": `{"command":"ls -la"}`,
		}},
		map[string]any{"type": "tool_result", "data": map[string]any{
			"tool_call_id": "call-1", "content": "total 0\n",
		}},
		map[string]any{"type": "shell_command", "data": map[string]any{
			"command":   "ls -la",
			"output":    "total 0\n",
			"exit_code": 0,
		}},
	)

	result, err := decodeParts(raw, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.events) != 1 {
		t.Fatalf("events = %d, want 1 (deduplicated)", len(result.events))
	}

	if result.events[0].ID != "call-1" {
		t.Fatalf("event ID = %q, want call-1 (tool call wins)", result.events[0].ID)
	}
}

// TestDecodePartsImageAndBinaryAreDropped verifies image_url and
// binary parts decode without error but produce no events or text.
// The visualizer renders file edits, not base64 previews.
func TestDecodePartsImageAndBinaryAreDropped(t *testing.T) {
	raw := writeParts(
		t,
		map[string]any{"type": "image_url", "data": map[string]any{"url": "data:image/png;base64,iVBORw0K"}},
		map[string]any{
			"type": "binary",
			"data": map[string]any{"data": "AAA=", "mime_type": "application/octet-stream"},
		},
	)

	result, err := decodeParts(raw, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.events) != 0 || result.text != "" {
		t.Fatalf("image/binary should produce no output, got %+v", result)
	}
}

// TestDecodePartsUnknownTypeIgnored guards the "schema bump does not
// crash older mindwalk" contract. A part with an unknown discriminator
// is silently dropped.
func TestDecodePartsUnknownTypeIgnored(t *testing.T) {
	raw := writeParts(t,
		map[string]any{"type": "totally_new_part", "data": map[string]any{"anything": "goes"}},
		map[string]any{"type": "text", "data": map[string]any{"text": "survives"}},
	)

	result, err := decodeParts(raw, "")
	if err != nil {
		t.Fatal(err)
	}

	if result.text != "survives" {
		t.Fatalf("text after unknown part = %q", result.text)
	}
}

// TestDecodePartsEmptyAndNullParts ensures the parser's short
// circuits for empty / null data don't crash and produce an empty
// result.
func TestDecodePartsEmptyAndNullParts(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"whitespace", "  \n"},
		{"empty array", "[]"},
		{"null data", `[{"type":"text","data":null}]`},
		{"missing type", `[{"data":{"text":"no type"}}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, err := decodeParts(c.raw, "")
			if err != nil {
				t.Fatal(err)
			}

			if result.text != "" || len(result.events) != 0 {
				t.Fatalf("expected empty result, got %+v", result)
			}
		})
	}
}

// TestDecodePartsToolCallWithoutIDDropped verifies a malformed
// tool_call (missing id or name) is dropped instead of crashing the
// whole trace. Crush occasionally writes such rows mid-write.
func TestDecodePartsToolCallWithoutIDDropped(t *testing.T) {
	cases := []map[string]any{
		{"type": "tool_call", "data": map[string]any{"name": "view", "input": "{}"}},
		{"type": "tool_call", "data": map[string]any{"id": "missing-name", "input": "{}"}},
	}

	encoded, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}

	result, err := decodeParts(string(encoded), "")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.events) != 0 {
		t.Fatalf("malformed tool_call should be dropped, got %+v", result.events)
	}
}

// TestDecodePartsToolResultWithoutIDDropped verifies a tool_result
// with no tool_call_id is dropped (orphan) instead of poisoning the
// pending calls.
func TestDecodePartsToolResultWithoutIDDropped(t *testing.T) {
	raw := writeParts(t,
		map[string]any{"type": "tool_result", "data": map[string]any{"content": "stray"}},
	)

	result, err := decodeParts(raw, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.events) != 0 || len(result.results) != 0 {
		t.Fatalf("expected empty result, got %+v", result)
	}
}

// TestDecodePartsToolCallIDCollision verifies that two tool calls
// with the same id collapse into one event. The parser keeps the
// most recent call's input and discards the older copy — the
// behaviour is intentional because the second call represents the
// authoritative "this is what the agent finally asked for". Crush
// can emit duplicate ids across messages when the cursor replays a
// row; the parser must not silently duplicate events.
func TestDecodePartsToolCallIDCollision(t *testing.T) {
	raw := writeParts(t,
		map[string]any{"type": "tool_call", "data": map[string]any{
			"id": "shared", "name": "view", "input": `{"file_path":"a.go"}`,
		}},
		map[string]any{"type": "tool_call", "data": map[string]any{
			"id": "shared", "name": "view", "input": `{"file_path":"b.go"}`,
		}},
		map[string]any{"type": "tool_result", "data": map[string]any{
			"tool_call_id": "shared", "content": "second result",
		}},
	)

	result, err := decodeParts(raw, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(result.events) != 1 {
		t.Fatalf("events = %d, want 1 (id collision merges)", len(result.events))
	}

	if result.events[0].Input["file_path"] != "b.go" {
		t.Fatalf("latest call wins, got file_path = %q", result.events[0].Input["file_path"])
	}

	if len(result.results) != 1 || result.results[0].Content != "second result" {
		t.Fatalf("results = %+v", result.results)
	}
}

// TestParseCrushInputNestedString covers the frequent Crush shape
// where tool_call.input is a JSON literal of another JSON object.
// The parser must peel the wrapping string and recover the object.
func TestParseCrushInputNestedString(t *testing.T) {
	// Single-layer nesting: the value stored in the SQLite column
	// is the literal JSON string `"{...}"`. json.Unmarshal of that
	// returns a Go string, which the parser then re-parses to
	// recover the inner object.
	inner := `{"file_path":"internal/server/server.go","limit":50}`

	wrapped, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}

	got := parseCrushInput(string(wrapped))
	if got["file_path"] != "internal/server/server.go" {
		t.Fatalf("nested JSON not peeled, got %+v", got)
	}
}

// TestParseCrushInputMalformedFallsBackToRaw covers the malformed
// input case: the parser must not panic and must still produce a
// map[string]any containing the original raw string under "_raw".
func TestParseCrushInputMalformedFallsBackToRaw(t *testing.T) {
	raw := "not json {"

	got := parseCrushInput(raw)
	if got["_raw"] != raw {
		t.Fatalf("expected _raw = %q, got %+v", raw, got)
	}
}

// TestAgentLabelFromInputPrefersTitle verifies the label extraction
// prefers task_title over description over prompt over message.
func TestAgentLabelFromInputPrefersTitle(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"task_title", map[string]any{"task_title": "first"}, "first"},
		{"title", map[string]any{"title": "second"}, "second"},
		{"description", map[string]any{"description": "third"}, "third"},
		{"prompt", map[string]any{"prompt": "fourth"}, "fourth"},
		{"message", map[string]any{"message": "fifth"}, "fifth"},
		{"empty", map[string]any{"message": "   "}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := agentLabelFromInput(c.input)
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestSplitAgentIDRoundTrip verifies the agent-tool id parser
// recovers the message id and tool call id from a `$$`-joined value
// and rejects malformed values.
func TestSplitAgentIDRoundTrip(t *testing.T) {
	msg, call, ok := splitAgentID("msg-1$$call-1")
	if !ok || msg != "msg-1" || call != "call-1" {
		t.Fatalf("split = (%q, %q, %v)", msg, call, ok)
	}

	bad := []string{"", "$$", "a$$", "$$b", "no-separator"}
	for _, b := range bad {
		if _, _, ok := splitAgentID(b); ok {
			t.Fatalf("expected false for %q", b)
		}
	}
}

// TestSplitSessionIDAcceptsBothFormats covers both the bare-id form
// and the crush://session/<id> synthetic form, returning the
// underlying session id either way.
func TestSplitSessionIDAcceptsBothFormats(t *testing.T) {
	for _, input := range []string{"abc-123", "crush://session/abc-123"} {
		id, _, ok := splitSessionID(input)
		if !ok || id != "abc-123" {
			t.Fatalf("input %q -> (%q, %v)", input, id, ok)
		}
	}

	if _, _, ok := splitSessionID(""); ok {
		t.Fatalf("empty input should fail")
	}
}

// TestSessionPathRoundTrip verifies the typed synthetic-path
// helpers in the adapter package. The scheme is shared with the
// server (which calls IsSessionPath), so the constant must stay
// stable.
func TestSessionPathRoundTrip(t *testing.T) {
	for _, id := range []string{"abc-123", "msg-1$$toolcall-1", "with/slash"} {
		got := SessionPath(id)
		if !IsSessionPath(got) {
			t.Fatalf("IsSessionPath(%q) = false", got)
		}

		if extracted := SessionIDFromPath(got); extracted != id {
			t.Fatalf("SessionIDFromPath(%q) = %q, want %q", got, extracted, id)
		}
	}

	if IsSessionPath("not a crush path") {
		t.Fatalf("non-crush path should fail IsSessionPath")
	}
	// SessionIDFromPath returns the input unchanged when the scheme
	// does not match — useful for callers that pass bare ids.
	if got := SessionIDFromPath("bare-id"); got != "bare-id" {
		t.Fatalf("SessionIDFromPath without scheme = %q, want bare-id", got)
	}
}
