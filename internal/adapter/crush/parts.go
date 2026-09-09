package crush

import (
	"fmt"
	"strings"

	crushdata "github.com/LarsArtmann/go-crush-data"
	"github.com/cosmtrek/mindwalk/internal/adapter"
)

// The raw on-disk shape of messages.parts (a discriminated
// {"type","data"} union), its decoding into typed values, and the
// schema-drift tolerance for unknown discriminators all live in the
// go-crush-data SDK. This file folds decoded parts into the
// mindwalk-specific trace view: tool calls paired with results,
// subagent launches, reasoning durations, finish reasons, and
// bang-mode shell commands.

// partsParser folds one message's decoded parts in declaration order.
// Tool calls and their results are paired by tool_call_id; any orphan
// result is kept for cross-message pairing by the agent graph reader,
// and any tool call without a matching result is emitted as an event
// with an empty ToolResult.
//
// State is held in the parser so callers can stream messages through
// one instance and keep the in-flight tool calls memory-cheap.
type partsParser struct {
	pending       map[string]adapter.ToolCall
	pendingOrder  []string
	results       map[string]adapter.ToolResult
	resultOrder   []string // tool_call_id for each tool_result part in declaration order
	text          strings.Builder
	subagentNote  string
	subagentSeen  bool
	hasUserFinish bool
	reasoningText string
	reasoningSecs int64
	finishReason  string
	finishMessage string
	shellCommands []crushdata.ShellCommandPart
	bashCommands  map[string]bool
}

// newPartsParser constructs an empty parser ready for one message.
func newPartsParser() *partsParser {
	return &partsParser{
		pending:      map[string]adapter.ToolCall{},
		results:      map[string]adapter.ToolResult{},
		bashCommands: map[string]bool{},
	}
}

// add folds one decoded part into the parser state. Parts the SDK
// could not classify arrive as UnknownPart and are ignored so a Crush
// schema bump never crashes an older mindwalk binary.
func (p *partsParser) add(part crushdata.Part, timestamp string) {
	switch typed := part.(type) {
	case crushdata.TextPart:
		if typed.Text != "" {
			if p.text.Len() > 0 {
				p.text.WriteString("\n")
			}

			p.text.WriteString(typed.Text)
		}
	case crushdata.ReasoningPart:
		if typed.Thinking != "" {
			p.reasoningText = typed.Thinking
		}

		if typed.FinishedAt > typed.StartedAt {
			p.reasoningSecs = typed.FinishedAt - typed.StartedAt
		}
	case crushdata.ShellCommandPart:
		p.shellCommands = append(p.shellCommands, typed)
	case crushdata.ToolCallPart:
		if typed.ID == "" || typed.Name == "" {
			return
		}

		input := parseCrushInput(typed.Input)

		call := adapter.ToolCall{
			ID:               typed.ID,
			Name:             typed.Name,
			Input:            input,
			Timestamp:        timestamp,
			ProviderExecuted: typed.ProviderExecuted,
		}
		if typed.Name == "agent" {
			p.subagentSeen = true
			if label := agentLabelFromInput(input); label != "" {
				p.subagentNote = label
			} else {
				p.subagentNote = typed.Name
			}
		}

		if _, exists := p.pending[typed.ID]; !exists {
			p.pendingOrder = append(p.pendingOrder, typed.ID)
		}

		p.pending[typed.ID] = call
		if typed.Name == "bash" || typed.Name == "Bash" {
			if cmd, ok := input["command"].(string); ok && cmd != "" {
				p.bashCommands[cmd] = true
			}
		}
	case crushdata.ToolResultPart:
		if typed.ToolCallID == "" {
			return
		}

		// IsError is decoded from the wire `is_error` field, so its
		// presence (true or false) means the tool result was observed
		// and the outcome is known.
		p.results[typed.ToolCallID] = adapter.ToolResult{
			ToolCallID:   typed.ToolCallID,
			Content:      typed.Content,
			IsError:      typed.IsError,
			OutcomeKnown: true,
		}
		p.resultOrder = append(p.resultOrder, typed.ToolCallID)
	case crushdata.FinishPart:
		if typed.Reason == "stop" {
			p.hasUserFinish = true
		}

		p.finishReason = typed.Reason
		p.finishMessage = typed.Message
	case crushdata.UnknownPart:
		// Image, binary, and future part kinds pass through as
		// UnknownPart — nothing here consumes their payloads.
	}
}

// agentLabelFromInput pulls the best label from an agent tool call's
// input — task title, then description, then the first non-empty
// string field.
func agentLabelFromInput(input map[string]any) string {
	for _, key := range []string{"task_title", "title", "description", "prompt", "message"} {
		if v, ok := input[key].(string); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}

	return ""
}

// parseCrushInput decodes a tool call input string (which Crush stores
// as raw JSON text in the SQLite column) into the map[string]any shape
// the existing actionFor/targetsFor helpers expect. The adapter never
// blocks on a malformed payload: it falls back to a single "_raw"
// entry so downstream heuristics can still mine it.
func parseCrushInput(raw string) map[string]any {
	return adapter.ParseJSONInput(raw)
}

// finishResult is the materialised trace pieces of one message:
//
//   - text:        the concatenated message body (used to detect user
//     turns and emit a user-message mark).
//   - events:      ordered tool-call events, each paired with its
//     result or an empty ToolResult when no result was
//     observed. Order matches declaration in the parts
//     JSON.
//   - subagent:    true when the message observed an `agent` tool
//     call — the caller records a subagent mark.
//   - userFinish:  true when the message ended with a finish reason
//     of `stop`, matching Crush's user-typed prompt shape
//     (the assistant finishes the user turn with `stop`).
type finishResult struct {
	text          string
	events        []adapter.ToolCall
	results       []adapter.ToolResult
	subagent      bool
	subagentNote  string
	userFinish    bool
	reasoningText string
	reasoningSecs int64
	finishReason  string
	finishMessage string
}

func (p *partsParser) finish() finishResult {
	result := finishResult{
		text:          strings.TrimSpace(p.text.String()),
		subagent:      p.subagentSeen,
		subagentNote:  p.subagentNote,
		userFinish:    p.hasUserFinish,
		reasoningText: p.reasoningText,
		reasoningSecs: p.reasoningSecs,
		finishReason:  p.finishReason,
		finishMessage: p.finishMessage,
	}
	// Surface every tool_call that landed in this message, paired
	// with its result when one was observed in the same message.
	// An unmatched tool_call gets an empty result so the timeline
	// does not silently lose the attempt. Remember which results
	// have been paired so the cross-message results loop below
	// doesn't double-emit them.
	paired := make(map[string]bool, len(p.results))
	for _, id := range p.pendingOrder {
		call := p.pending[id]

		result.events = append(result.events, call)
		if r, ok := p.results[id]; ok {
			result.results = append(result.results, r)
			paired[id] = true
		} else {
			result.results = append(result.results, adapter.ToolResult{})
		}
	}
	// Append tool results whose originating tool_call lives in a
	// different message — they have no event in this message but
	// the agent graph reader needs them to pair launches across
	// messages. Each result carries its own ToolCallID so the
	// consumer can match without a parallel slice.
	for _, callID := range p.resultOrder {
		if paired[callID] {
			continue
		}

		result.results = append(result.results, p.results[callID])
	}
	// Emit bang-mode shell commands as exec events, but skip any
	// whose command string was already captured by a bash tool call
	// in the same message to avoid duplicates.
	for i, sc := range p.shellCommands {
		if sc.Command != "" && p.bashCommands[sc.Command] {
			continue
		}

		call := adapter.ToolCall{
			ID:        shellEventID(i),
			Name:      "bash",
			Input:     map[string]any{"command": sc.Command},
			Timestamp: "",
		}
		result.events = append(result.events, call)
		result.results = append(result.results, adapter.ToolResult{
			ToolCallID:   call.ID,
			Content:      sc.Output,
			IsError:      sc.ExitCode != 0,
			OutcomeKnown: true,
		})
	}

	return result
}

// shellEventID builds the synthetic event id for the i-th bang-mode
// shell command.
func shellEventID(i int) string {
	return fmt.Sprintf("shell-%d", i)
}

// foldParts folds one message's decoded parts into its trace pieces.
// Nil parts (empty payload, or JSON the SDK refused) yield the zero
// result — a single corrupted message never poisons the trace.
func foldParts(parts []crushdata.Part, timestamp string) finishResult {
	parser := newPartsParser()

	for _, part := range parts {
		parser.add(part, timestamp)
	}

	return parser.finish()
}

// decodeParts parses a single messages.parts JSON string and folds it
// into the trace view. Production reads flow through the SDK's
// already-decoded [crushdata.Message.Parts]; this composition serves
// callers that hold the raw column text.
func decodeParts(raw string, timestamp string) (finishResult, error) {
	parts, err := crushdata.DecodeParts(raw)
	if err != nil {
		return finishResult{}, err
	}

	return foldParts(parts, timestamp), nil
}
