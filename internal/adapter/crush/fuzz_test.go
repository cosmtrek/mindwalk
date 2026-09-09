package crush

import "testing"

// FuzzDecodeParts verifies that arbitrary JSON input never panics the
// parts decoder. The decoder must handle malformed, truncated, and
// adversarial JSON gracefully.
func FuzzDecodeParts(f *testing.F) {
	f.Add(`[{"type":"text","data":{"text":"hello"}}]`)
	f.Add(`[{"type":"tool_call","data":{"id":"c1","name":"read","input":"{}"}}]`)
	f.Add(`[]`)
	f.Add(`malformed`)
	f.Add(`{"not":"array"}`)
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = decodeParts(raw, "")
		// The invariant: decodeParts must not panic on any input.
	})
}

// FuzzSplitAgentID verifies the agent-id parser never panics.
func FuzzSplitAgentID(f *testing.F) {
	f.Add("m_assistant_1$$call_agent_1")
	f.Add("simple")
	f.Add("$$")
	f.Add("")
	f.Add("a$$b$$c")
	f.Fuzz(func(t *testing.T, id string) {
		splitAgentID(id)
	})
}
