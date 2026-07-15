package claudecode

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
	return filepath.Join(home, ".claude", "projects")
}

func (a Adapter) Harness() string {
	return "claude-code"
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
		if filepath.Ext(path) != ".jsonl" || strings.HasPrefix(filepath.Base(path), "agent-") {
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
	f, err := os.Open(path)
	if err != nil {
		return model.SessionMeta{}, err
	}
	defer f.Close()

	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	meta := model.SessionMeta{Key: adapter.SessionKey(a.Harness(), path), ID: id, Harness: a.Harness(), Path: path}
	recognized := false
	err = adapter.ReadJSONLines(f, func(data []byte) {
		var line rawLine
		if json.Unmarshal(data, &line) != nil {
			return
		}
		if isClaudeLine(line) {
			recognized = true
		}
		if line.IsSidechain {
			meta.Auxiliary = true
			if meta.Agent == nil {
				meta.Agent = &model.AgentSessionMeta{}
			}
			if line.AgentID != "" {
				meta.ID = line.AgentID
				meta.Agent.SourceID = line.AgentID
			}
			if line.SessionID != "" {
				meta.Agent.RootSessionID = line.SessionID
			}
		} else if line.SessionID != "" && !meta.Auxiliary {
			meta.ID = line.SessionID
		}
		if line.Timestamp != "" {
			if meta.StartedAt == "" {
				meta.StartedAt = line.Timestamp
			}
			meta.EndedAt = line.Timestamp
		}
		if line.Type == "ai-title" && line.AITitle != "" {
			meta.Title = line.AITitle
		}
		if line.Cwd != "" && meta.Cwd == "" {
			meta.Cwd = line.Cwd
		}
		if line.GitBranch != "" && meta.GitBranch == "" {
			meta.GitBranch = line.GitBranch
		}
		if len(line.Message) > 0 {
			var msg message
			if json.Unmarshal(line.Message, &msg) == nil {
				if msg.Model != "" && meta.Model == "" {
					meta.Model = msg.Model
				}
				meta.EventCount += countToolUses(msg.Content)
				// mirrors Parse's user-message mark filter so the badge's
				// staleness check counts the same turns the report will
				if line.Type == "user" && hasUserMessage(msg.Content) && !adapter.InjectedUserMessage(userMessageText(msg.Content)) {
					meta.UserTurns++
				}
			}
		}
	})
	if meta.Auxiliary {
		var childMeta struct {
			AgentType   string `json:"agentType"`
			Description string `json:"description"`
			ToolUseID   string `json:"toolUseId"`
			SpawnDepth  int    `json:"spawnDepth"`
		}
		data, readErr := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".meta.json")
		if readErr == nil && json.Unmarshal(data, &childMeta) == nil {
			meta.Agent.Depth = childMeta.SpawnDepth
			meta.Agent.Label = childMeta.Description
			meta.Agent.Role = childMeta.AgentType
			meta.Agent.LaunchCallID = childMeta.ToolUseID
		}
	}
	if meta.Title == "" {
		meta.Title = filepath.Base(path)
	}
	if !recognized {
		return model.SessionMeta{}, fmt.Errorf("not a Claude Code session: %s", path)
	}
	return meta, err
}

func (a Adapter) Parse(path string) (*model.Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

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

	recognized := false
	pending := map[string]adapter.ToolCall{}
	pendingOrder := []string{}
	err = adapter.ReadJSONLines(f, func(data []byte) {
		var line rawLine
		if json.Unmarshal(data, &line) != nil {
			return
		}
		if isClaudeLine(line) {
			recognized = true
		}
		applyLineMeta(trace, line)
		if line.Type == "ai-title" && line.AITitle != "" {
			trace.Session.Title = line.AITitle
			return
		}
		if isCompaction(line) {
			trace.Marks = append(trace.Marks, model.Mark{Seq: len(trace.Events), Type: "compaction"})
		}
		if len(line.Message) == 0 {
			return
		}

		var msg message
		if json.Unmarshal(line.Message, &msg) != nil {
			return
		}
		if line.Type == "user" && hasUserMessage(msg.Content) {
			if text := userMessageText(msg.Content); !adapter.InjectedUserMessage(text) {
				trace.Marks = append(trace.Marks, model.Mark{
					Seq:  len(trace.Events),
					Type: "user-message",
					Note: adapter.UserMessageNote(text),
				})
			}
		}
		if msg.Model != "" && trace.Session.Model == "" {
			trace.Session.Model = msg.Model
		}
		for _, item := range msg.Content.Items {
			switch item.Type {
			case "tool_use":
				call := adapter.ToolCall{
					ID:        item.ID,
					Name:      item.Name,
					Input:     item.Input,
					Timestamp: line.Timestamp,
				}
				if call.Name == "Task" || call.Name == "Agent" {
					trace.Marks = append(trace.Marks, model.Mark{Seq: len(trace.Events), Type: "subagent", Note: call.Name})
				}
				if _, exists := pending[call.ID]; !exists {
					pendingOrder = append(pendingOrder, call.ID)
				}
				pending[call.ID] = call
			case "tool_result":
				call, ok := pending[item.ToolUseID]
				if !ok {
					continue
				}
				delete(pending, item.ToolUseID)
				event := buildEvent(trace, call, item)
				trace.Events = append(trace.Events, event)
			}
		}
	})
	for _, id := range pendingOrder {
		if call, ok := pending[id]; ok {
			trace.Events = append(trace.Events, buildEvent(trace, call, contentItem{}))
		}
	}
	sort.Slice(trace.Events, func(i, j int) bool {
		return trace.Events[i].Seq < trace.Events[j].Seq
	})
	for i := range trace.Events {
		trace.Events[i].Seq = i
	}
	trace.Session.EventCount = len(trace.Events)
	// Claude Code tool results carry an is_error flag set by the harness.
	trace.Stats = model.ComputeStats(trace, 0, model.ObservabilityExact)
	if !recognized {
		return nil, fmt.Errorf("not a Claude Code session: %s", path)
	}
	return trace, err
}

type rawLine struct {
	Type        string          `json:"type"`
	Timestamp   string          `json:"timestamp"`
	SessionID   string          `json:"sessionId"`
	AgentID     string          `json:"agentId"`
	IsSidechain bool            `json:"isSidechain"`
	Cwd         string          `json:"cwd"`
	GitBranch   string          `json:"gitBranch"`
	Message     json.RawMessage `json:"message"`
	AITitle     string          `json:"aiTitle"`
	Content     string          `json:"content"`
	Subtype     string          `json:"subtype"`
}

type message struct {
	Role    string      `json:"role"`
	Model   string      `json:"model"`
	Content contentList `json:"content"`
}

type contentList struct {
	Items []contentItem
}

func (c *contentList) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		c.Items = []contentItem{{Type: "text", Text: s}}
		return nil
	}
	var items []contentItem
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	c.Items = items
	return nil
}

type contentItem struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
	Content   any            `json:"content"`
	IsError   bool           `json:"is_error"`
	Text      string         `json:"text"`
}

func countToolUses(content contentList) int {
	count := 0
	for _, item := range content.Items {
		if item.Type == "tool_use" {
			count++
		}
	}
	return count
}

func userMessageText(content contentList) string {
	var parts []string
	for _, item := range content.Items {
		if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
			parts = append(parts, strings.TrimSpace(item.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func hasUserMessage(content contentList) bool {
	if len(content.Items) == 0 {
		return false
	}
	for _, item := range content.Items {
		if item.Type == "tool_result" {
			return false
		}
		if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
			return true
		}
	}
	return true
}

func applyLineMeta(trace *model.Trace, line rawLine) {
	if line.SessionID != "" {
		trace.Session.ID = line.SessionID
	}
	if line.Cwd != "" && trace.Session.Cwd == "" {
		trace.Session.Cwd = line.Cwd
	}
	if line.Timestamp != "" {
		if trace.Session.StartedAt == "" {
			trace.Session.StartedAt = line.Timestamp
		}
		trace.Session.EndedAt = line.Timestamp
	}
}

func isCompaction(line rawLine) bool {
	return line.Type == "system" && strings.Contains(strings.ToLower(line.Subtype), "compact")
}

func isClaudeLine(line rawLine) bool {
	if line.SessionID != "" {
		return true
	}
	switch line.Type {
	case "user", "assistant", "system", "ai-title":
		return line.Timestamp != "" || len(line.Message) > 0
	default:
		return false
	}
}

func buildEvent(trace *model.Trace, call adapter.ToolCall, result contentItem) model.Event {
	return adapter.BuildEvent(trace, call, adapter.ToolResult{
		Content: adapter.ContentToString(result.Content),
		IsError: result.IsError,
	})
}
