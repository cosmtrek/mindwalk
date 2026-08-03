package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/model"
)

type Adapter struct {
	Dir string
}

func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".copilot", "sessions")
}

func (a Adapter) Harness() string {
	return "copilot"
}

func (a Adapter) SessionDir() string {
	if a.Dir != "" {
		return a.Dir
	}
	return DefaultDir()
}

func (a Adapter) ListSessions() ([]model.SessionMeta, error) {
	dir := a.SessionDir()
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, nil
	}
	var metas []model.SessionMeta
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".jsonl" {
			return nil
		}
		meta, err := a.Summarize(path)
		if err == nil {
			metas = append(metas, meta)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].EndedAt > metas[j].EndedAt
	})
	return metas, nil
}

func (a Adapter) Summarize(path string) (model.SessionMeta, error) {
	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	meta := model.SessionMeta{
		Key:     adapter.SessionKey(a.Harness(), path),
		ID:      id,
		Harness: a.Harness(),
		Path:    path,
	}

	f, err := os.Open(path)
	if err != nil {
		return model.SessionMeta{}, err
	}
	defer f.Close()

	recognized := false
	err = adapter.ReadJSONLines(f, func(data []byte) {
		var line copilotLine
		if json.Unmarshal(data, &line) != nil {
			return
		}
		if !isCopilotLine(line) {
			return
		}
		recognized = true
		if line.SessionID != "" && meta.ID == id {
			meta.ID = line.SessionID
		}
		if line.Timestamp != "" {
			if meta.StartedAt == "" {
				meta.StartedAt = line.Timestamp
			}
			meta.EndedAt = line.Timestamp
		}
		if line.Cwd != "" && meta.Cwd == "" {
			meta.Cwd = line.Cwd
		}
		if line.Model != "" && meta.Model == "" {
			meta.Model = line.Model
		}
		switch line.Type {
		case "user":
			text := copilotContentText(line.Message)
			if !adapter.InjectedUserMessage(text) {
				meta.UserTurns++
			}
			meta.EventCount += countToolCalls(line.Message)
		case "assistant":
			meta.EventCount += countToolCalls(line.Message)
		}
	})
	if err != nil {
		return model.SessionMeta{}, err
	}
	if !recognized {
		return model.SessionMeta{}, fmt.Errorf("not a copilot session: %s", path)
	}
	if meta.Title == "" {
		meta.Title = filepath.Base(path)
	}
	return meta, nil
}

func (a Adapter) Parse(path string) (*model.Trace, error) {
	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	trace := &model.Trace{
		Version: 1,
		Session: model.TraceSession{
			ID:      id,
			Harness: a.Harness(),
			Path:    path,
		},
		Events: []model.Event{},
		Marks:  []model.Mark{},
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	recognized := false
	pending := map[string]adapter.ToolCall{}
	pendingOrder := []string{}
	firstUserText := ""

	err = adapter.ReadJSONLines(f, func(data []byte) {
		var line copilotLine
		if json.Unmarshal(data, &line) != nil {
			return
		}
		if !isCopilotLine(line) {
			return
		}
		recognized = true

		if line.SessionID != "" && trace.Session.ID == id {
			trace.Session.ID = line.SessionID
		}
		if line.Timestamp != "" {
			if trace.Session.StartedAt == "" {
				trace.Session.StartedAt = line.Timestamp
			}
			trace.Session.EndedAt = line.Timestamp
		}
		if line.Cwd != "" && trace.Session.Cwd == "" {
			trace.Session.Cwd = line.Cwd
		}
		if line.Model != "" && trace.Session.Model == "" {
			trace.Session.Model = line.Model
		}

		switch line.Type {
		case "user":
			text := copilotContentText(line.Message)
			if !adapter.InjectedUserMessage(text) {
				trace.Marks = append(trace.Marks, model.Mark{
					Seq:  len(trace.Events),
					Type: "user-message",
					Note: adapter.UserMessageNote(text),
				})
				if firstUserText == "" {
					firstUserText = text
				}
			}
			// Tool results from the harness arrive as user messages
			// with tool_result content blocks — resolve them against
			// pending tool calls.
			for _, block := range copilotContentBlocks(line.Message) {
				if block.Type != "toolResult" || block.ID == "" {
					continue
				}
				call, ok := pending[block.ID]
				if !ok {
					continue
				}
				delete(pending, block.ID)
				trace.Events = append(trace.Events, adapter.BuildEvent(trace, call, adapter.ToolResult{
					Content: copilotToolResultContent(block),
					IsError: block.IsError,
				}))
			}
		case "assistant":
			for _, block := range copilotContentBlocks(line.Message) {
				if block.Type != "toolCall" || block.ID == "" {
					continue
				}
				call := adapter.ToolCall{
					ID:        block.ID,
					Name:      block.Name,
					Input:     block.Arguments,
					Timestamp: line.Timestamp,
				}
				if _, exists := pending[call.ID]; !exists {
					pendingOrder = append(pendingOrder, call.ID)
				}
				pending[call.ID] = call
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !recognized {
		return nil, fmt.Errorf("not a copilot session: %s", path)
	}

	// Any tool calls without results get empty results.
	for _, id := range pendingOrder {
		if call, ok := pending[id]; ok {
			trace.Events = append(trace.Events, adapter.BuildEvent(trace, call, adapter.ToolResult{}))
		}
	}

	trace.Session.Title = sessionTitle(firstUserText, path)
	trace.Session.EventCount = len(trace.Events)
	trace.Stats = model.ComputeStats(trace, 0, model.ObservabilityExact)
	return trace, nil
}

// copilotLine represents a single line in a Copilot CLI session JSONL file.
type copilotLine struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	Model     string          `json:"model"`
	Message   json.RawMessage `json:"message"`
}

// isCopilotLine returns true if the line looks like a Copilot CLI session entry.
func isCopilotLine(line copilotLine) bool {
	return line.Type == "user" || line.Type == "assistant" || line.Type == "system"
}

// copilotContentBlocks parses the message content field into blocks.
func copilotContentBlocks(raw json.RawMessage) []copilotContentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []copilotContentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		return blocks
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return []copilotContentBlock{{Type: "text", Text: s}}
	}
	return nil
}

// copilotContentText extracts the text content from a message,
// joining text blocks and ignoring tool calls/results.
func copilotContentText(raw json.RawMessage) string {
	var parts []string
	for _, block := range copilotContentBlocks(raw) {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n")
}

// copilotToolResultContent extracts the content from a tool result block.
func copilotToolResultContent(block copilotContentBlock) string {
	if block.Type != "toolResult" {
		return ""
	}
	return block.Content
}

// countToolCalls counts the tool calls that Parse will turn into events.
func countToolCalls(raw json.RawMessage) int {
	count := 0
	for _, block := range copilotContentBlocks(raw) {
		if block.Type == "toolCall" && block.ID != "" {
			count++
		}
	}
	return count
}

type copilotContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Content   string         `json:"content"`
	IsError   bool           `json:"isError"`
}

// sessionTitle returns a title for the session, using the first user
// message text or falling back to the file name.
func sessionTitle(firstUserText, path string) string {
	if firstUserText != "" {
		return adapter.AgentInstructionPreview(firstUserText)
	}
	return filepath.Base(path)
}