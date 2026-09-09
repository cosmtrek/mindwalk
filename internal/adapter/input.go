package adapter

import (
	"encoding/json"
	"strings"
)

// ParseJSONInput normalizes a tool-call input payload (which every
// adapter stores as a raw JSON string in its native log format) into
// the map[string]any shape the actionFor/targetsFor helpers expect.
//
// The function is forgiving by design: tool inputs are user-facing
// strings that the harness never validates before persistence, so
// malformed payloads must not block trace rendering. The fallback
// strategy is:
//
//   - empty / whitespace-only → empty map (caller treats as no input)
//   - JSON object → returned as the decoded map
//   - JSON string → re-parsed recursively (Crush's view tool nests a
//     one-key object as a JSON literal, codex does the same for some
//     apply_patch payloads)
//   - other JSON value → re-encoded as a "_raw" entry so downstream
//     heuristics can still mine the bytes
//   - malformed → original text preserved as a "_raw" entry
//
// The same rule is reused by every adapter; the per-adapter helpers
// in codex and crush used to re-implement this loop with slight
// variations, all of which converged on the same behaviour.
func ParseJSONInput(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}

	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return map[string]any{"_raw": raw}
	}

	switch v := value.(type) {
	case map[string]any:
		return v
	case string:
		return ParseJSONInput(v)
	default:
		encoded, _ := json.Marshal(value)

		return map[string]any{"_raw": string(encoded)}
	}
}
