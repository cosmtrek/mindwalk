package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/judge"
	"github.com/cosmtrek/mindwalk/internal/model"
)

type Adapter struct {
	Dir       string
	IndexPath string
}

func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

func DefaultIndexPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "session_index.jsonl")
}

func (a Adapter) Harness() string {
	return "codex"
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
		if err == nil && !meta.Auxiliary {
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
	sawSessionMeta := false
	seenCalls := map[string]bool{}
	err = adapter.ReadJSONLines(f, func(data []byte) {
		var line rawLine
		if json.Unmarshal(data, &line) != nil {
			return
		}
		if line.Timestamp != "" {
			if meta.StartedAt == "" {
				meta.StartedAt = line.Timestamp
			}
			meta.EndedAt = line.Timestamp
		}
		switch line.Type {
		case "session_meta":
			recognized = true
			var payload sessionMetaPayload
			if json.Unmarshal(line.Payload, &payload) == nil {
				firstSessionMeta := !sawSessionMeta
				if !sawSessionMeta {
					sawSessionMeta = true
					meta.Auxiliary = payload.isSubagent()
				}
				applySessionMeta(&meta, payload, firstSessionMeta)
			}
		case "turn_context":
			recognized = true
			var payload turnContextPayload
			if json.Unmarshal(line.Payload, &payload) == nil {
				if payload.Cwd != "" && meta.Cwd == "" {
					meta.Cwd = payload.Cwd
				}
				if payload.Model != "" && meta.Model == "" {
					meta.Model = payload.Model
				}
			}
		case "response_item":
			recognized = true
			var payload responseItemHeader
			if json.Unmarshal(line.Payload, &payload) == nil {
				if callID, ok := canonicalCallID(payload.Type, payload.ID, payload.CallID, payload.Name); ok && !seenCalls[callID] {
					seenCalls[callID] = true
					meta.EventCount++
				}
				// only message items pay the full decode; the count must
				// mirror Parse's user-message mark filter
				if payload.Type == "message" {
					var msg responseItemPayload
					if json.Unmarshal(line.Payload, &msg) == nil && msg.Role == "user" && msg.Content.HasText() {
						if !adapter.InjectedUserMessage(msg.Content.Text()) {
							meta.UserTurns++
						}
					}
				}
			}
		case "event_msg":
			recognized = true
		case "message":
			recognized = true
			if line.Role == "user" && line.Content.HasText() {
				if !adapter.InjectedUserMessage(line.Content.Text()) {
					meta.UserTurns++
				}
			}
		case "":
			if line.ID != "" {
				recognized = true
				meta.ID = line.ID
			}
		}
	})
	if judge.IsWorkDir(meta.Cwd) {
		// Sessions recorded in the judge workdir are mindwalk's own judge
		// runs (codex cannot disable session persistence), not user coding
		// sessions; auxiliary keeps them out of every listing.
		meta.Auxiliary = true
	}
	if meta.Title == "" {
		meta.Title = a.titleFor(meta.ID)
	}
	if meta.Title == "" {
		meta.Title = filepath.Base(path)
	}
	if !recognized {
		return model.SessionMeta{}, fmt.Errorf("not a Codex session: %s", path)
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
	sawSessionMeta := false
	calls := map[string]adapter.ToolCall{}
	callOrder := []string{}
	results := map[string]adapter.ToolResult{}
	directPatches := map[string]bool{}
	patchResults := map[string]patchApplyEndPayload{}
	err = adapter.ReadJSONLines(f, func(data []byte) {
		var line rawLine
		if json.Unmarshal(data, &line) != nil {
			return
		}
		applyLineTime(trace, line.Timestamp)
		switch line.Type {
		case "session_meta":
			recognized = true
			var payload sessionMetaPayload
			if json.Unmarshal(line.Payload, &payload) == nil {
				firstSessionMeta := !sawSessionMeta
				sawSessionMeta = true
				applyTraceSessionMeta(trace, payload, firstSessionMeta)
			}
		case "turn_context":
			recognized = true
			var payload turnContextPayload
			if json.Unmarshal(line.Payload, &payload) == nil {
				if payload.Cwd != "" && trace.Session.Cwd == "" {
					trace.Session.Cwd = payload.Cwd
				}
				if payload.Model != "" && trace.Session.Model == "" {
					trace.Session.Model = payload.Model
				}
			}
		case "response_item":
			recognized = true
			var payload responseItemPayload
			if json.Unmarshal(line.Payload, &payload) != nil {
				return
			}
			if call, source, ok := decodeCall(payload, line.Timestamp); ok {
				if _, exists := calls[call.ID]; exists {
					return
				}
				if call.Name == "spawn_agent" {
					trace.Marks = append(trace.Marks, model.Mark{Seq: len(callOrder), Type: "subagent", Note: call.Name})
				}
				calls[call.ID] = call
				callOrder = append(callOrder, call.ID)
				directPatches[call.ID] = source == "custom_tool_call" && call.Name == "apply_patch"
				return
			}
			if callID, result, ok := decodeOutput(payload); ok {
				if _, exists := calls[callID]; !exists {
					return
				}
				if _, exists := results[callID]; exists {
					return
				}
				results[callID] = result
				return
			}
			if payload.Type == "message" {
				if payload.Role == "user" && payload.Content.HasText() {
					if text := payload.Content.Text(); !adapter.InjectedUserMessage(text) {
						trace.Marks = append(trace.Marks, model.Mark{
							Seq:  len(callOrder),
							Type: "user-message",
							Note: adapter.UserMessageNote(text),
						})
					}
				}
			}
		case "message":
			recognized = true
			if line.Role == "user" && line.Content.HasText() {
				if text := line.Content.Text(); !adapter.InjectedUserMessage(text) {
					trace.Marks = append(trace.Marks, model.Mark{
						Seq:  len(callOrder),
						Type: "user-message",
						Note: adapter.UserMessageNote(text),
					})
				}
			}
		case "event_msg":
			recognized = true
			var payload patchApplyEndPayload
			if json.Unmarshal(line.Payload, &payload) != nil {
				return
			}
			if payload.Type == "context_compacted" {
				trace.Marks = append(trace.Marks, model.Mark{Seq: len(callOrder), Type: "compaction"})
				return
			}
			if payload.Type != "patch_apply_end" || payload.CallID == "" {
				return
			}
			if !directPatches[payload.CallID] {
				return
			}
			if _, exists := patchResults[payload.CallID]; !exists {
				patchResults[payload.CallID] = payload
			}
		case "":
			if line.ID != "" {
				recognized = true
				trace.Session.ID = line.ID
			}
		}
	})
	for _, id := range callOrder {
		call := calls[id]
		result := results[id]
		if patchResult, ok := patchResults[id]; ok {
			call.Input = applyPatchChanges(call.Input, patchResult.Changes)
			if patchResult.Success != nil {
				result.IsError = !*patchResult.Success
			}
		}
		trace.Events = append(trace.Events, adapter.BuildEvent(trace, call, result))
	}
	for i := range trace.Events {
		trace.Events[i].Seq = i
	}
	if trace.Session.Title == "" {
		trace.Session.Title = a.titleFor(trace.Session.ID)
	}
	if trace.Session.Title == "" {
		trace.Session.Title = filepath.Base(path)
	}
	trace.Session.EventCount = len(trace.Events)
	// Codex logs carry no structural error flag; failures are inferred from
	// output text, and inner commands of the js exec runner surface only what
	// the script chooses to print.
	trace.Stats = model.ComputeStats(trace, 0, model.ObservabilityEstimated)
	if !recognized {
		return nil, fmt.Errorf("not a Codex session: %s", path)
	}
	return trace, err
}

type rawLine struct {
	Type      string           `json:"type"`
	Timestamp string           `json:"timestamp"`
	Payload   json.RawMessage  `json:"payload"`
	ID        string           `json:"id"`
	Role      string           `json:"role"`
	Content   codexContentList `json:"content"`
}

type sessionMetaPayload struct {
	ID             string          `json:"id"`
	SessionID      string          `json:"session_id"`
	ParentThreadID string          `json:"parent_thread_id"`
	AgentPath      string          `json:"agent_path"`
	AgentNickname  string          `json:"agent_nickname"`
	AgentRole      string          `json:"agent_role"`
	Timestamp      string          `json:"timestamp"`
	Cwd            string          `json:"cwd"`
	ThreadSource   string          `json:"thread_source"`
	Source         json.RawMessage `json:"source"`
	Git            struct {
		Branch     string `json:"branch"`
		CommitHash string `json:"commit_hash"`
	} `json:"git"`
}

func (p sessionMetaPayload) agentMeta() *model.AgentSessionMeta {
	if !p.isSubagent() {
		return nil
	}
	agent := &model.AgentSessionMeta{
		SourceID:        p.ID,
		RootSessionID:   p.SessionID,
		ParentSessionID: p.ParentThreadID,
		AgentPath:       p.AgentPath,
		Label:           p.AgentNickname,
		Role:            p.AgentRole,
	}
	var source struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	var subagent struct {
		ThreadSpawn struct {
			ParentThreadID string `json:"parent_thread_id"`
			Depth          int    `json:"depth"`
			AgentPath      string `json:"agent_path"`
			AgentNickname  string `json:"agent_nickname"`
			AgentRole      string `json:"agent_role"`
		} `json:"thread_spawn"`
	}
	if json.Unmarshal(p.Source, &source) == nil && json.Unmarshal(source.Subagent, &subagent) == nil {
		agent.Depth = subagent.ThreadSpawn.Depth
		if agent.AgentPath == "" {
			agent.AgentPath = subagent.ThreadSpawn.AgentPath
		}
		if agent.ParentSessionID == "" {
			agent.ParentSessionID = subagent.ThreadSpawn.ParentThreadID
		}
		if agent.Label == "" {
			agent.Label = subagent.ThreadSpawn.AgentNickname
		}
		if agent.Role == "" {
			agent.Role = subagent.ThreadSpawn.AgentRole
		}
	}
	return agent
}

func (p sessionMetaPayload) isSubagent() bool {
	if p.ThreadSource == "subagent" {
		return true
	}
	if len(p.Source) == 0 {
		return false
	}
	var source struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal(p.Source, &source) != nil {
		return false
	}
	subagent := strings.TrimSpace(string(source.Subagent))
	return subagent != "" && subagent != "null"
}

type turnContextPayload struct {
	Cwd   string `json:"cwd"`
	Model string `json:"model"`
}

type responseItemPayload struct {
	Type      string           `json:"type"`
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Arguments json.RawMessage  `json:"arguments"`
	Input     json.RawMessage  `json:"input"`
	CallID    string           `json:"call_id"`
	Output    any              `json:"output"`
	Role      string           `json:"role"`
	Content   codexContentList `json:"content"`
}

// responseItemHeader keeps session-list scans allocation-light. Summarize only
// needs call identity, so decoding large arguments, outputs, and patch bodies
// here would add substantial cold-start cost without changing the count.
type responseItemHeader struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	CallID string `json:"call_id"`
}

type patchApplyEndPayload struct {
	Type    string                      `json:"type"`
	CallID  string                      `json:"call_id"`
	Success *bool                       `json:"success"`
	Changes map[string]patchApplyChange `json:"changes"`
}

type patchApplyChange struct {
	Type string `json:"type"`
}

type codexContentList struct {
	Items []codexContentItem
}

func (c *codexContentList) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		c.Items = []codexContentItem{{Type: "text", Text: s}}
		return nil
	}
	var items []codexContentItem
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	c.Items = items
	return nil
}

func (c codexContentList) Text() string {
	var parts []string
	for _, item := range c.Items {
		if strings.TrimSpace(item.Text) != "" {
			parts = append(parts, strings.TrimSpace(item.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func (c codexContentList) HasText() bool {
	for _, item := range c.Items {
		if strings.TrimSpace(item.Text) != "" {
			return true
		}
	}
	return false
}

type codexContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func applySessionMeta(meta *model.SessionMeta, payload sessionMetaPayload, setIdentity bool) {
	if setIdentity {
		if payload.ID != "" {
			meta.ID = payload.ID
		} else if payload.SessionID != "" {
			meta.ID = payload.SessionID
		}
		meta.Agent = payload.agentMeta()
	}
	if payload.Cwd != "" && meta.Cwd == "" {
		meta.Cwd = payload.Cwd
	}
	if payload.Git.Branch != "" && meta.GitBranch == "" {
		meta.GitBranch = payload.Git.Branch
	}
	if payload.Timestamp != "" {
		if meta.StartedAt == "" {
			meta.StartedAt = payload.Timestamp
		}
		if meta.EndedAt == "" {
			meta.EndedAt = payload.Timestamp
		}
	}
}

func applyTraceSessionMeta(trace *model.Trace, payload sessionMetaPayload, setIdentity bool) {
	if setIdentity {
		if payload.ID != "" {
			trace.Session.ID = payload.ID
		} else if payload.SessionID != "" {
			trace.Session.ID = payload.SessionID
		}
	}
	if payload.Cwd != "" && trace.Session.Cwd == "" {
		trace.Session.Cwd = payload.Cwd
	}
	if payload.Git.CommitHash != "" && trace.Session.Commit == "" {
		trace.Session.Commit = payload.Git.CommitHash
	}
	if payload.Timestamp != "" && trace.Session.StartedAt == "" {
		trace.Session.StartedAt = payload.Timestamp
	}
}

func applyLineTime(trace *model.Trace, ts string) {
	if ts == "" {
		return
	}
	if trace.Session.StartedAt == "" {
		trace.Session.StartedAt = ts
	}
	trace.Session.EndedAt = ts
}

func decodeCall(payload responseItemPayload, timestamp string) (adapter.ToolCall, string, bool) {
	callID, ok := canonicalCallID(payload.Type, payload.ID, payload.CallID, payload.Name)
	if !ok {
		return adapter.ToolCall{}, "", false
	}
	var raw json.RawMessage
	switch payload.Type {
	case "function_call":
		raw = payload.Arguments
	case "custom_tool_call":
		raw = payload.Input
	}
	return adapter.ToolCall{
		ID:        callID,
		Name:      payload.Name,
		Input:     parseInput(raw),
		Timestamp: timestamp,
	}, payload.Type, true
}

func canonicalCallID(callType, id, callID, name string) (string, bool) {
	switch callType {
	case "function_call", "custom_tool_call":
	default:
		return "", false
	}
	if callID == "" {
		callID = id
	}
	return callID, callID != "" && name != ""
}

func decodeOutput(payload responseItemPayload) (string, adapter.ToolResult, bool) {
	switch payload.Type {
	case "function_call_output", "custom_tool_call_output":
	default:
		return "", adapter.ToolResult{}, false
	}
	if payload.CallID == "" {
		return "", adapter.ToolResult{}, false
	}
	output := adapter.ContentToString(payload.Output)
	return payload.CallID, adapter.ToolResult{
		Content: output,
		IsError: commandOutputFailed(output),
	}, true
}

func parseInput(raw json.RawMessage) map[string]any {
	input := map[string]any{}
	if len(raw) == 0 || string(raw) == "null" {
		return input
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		input["_raw"] = string(raw)
		return input
	}
	switch value := value.(type) {
	case map[string]any:
		return value
	case string:
		return parseInputText(value)
	default:
		encoded, _ := json.Marshal(value)
		input["_raw"] = string(encoded)
		return input
	}
}

func parseInputText(text string) map[string]any {
	input := map[string]any{}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return input
	}
	if json.Unmarshal([]byte(trimmed), &input) == nil {
		return input
	}
	var nested string
	if json.Unmarshal([]byte(trimmed), &nested) == nil && nested != text {
		return parseInputText(nested)
	}
	input["_raw"] = text
	return input
}

func applyPatchChanges(input map[string]any, changes map[string]patchApplyChange) map[string]any {
	if len(changes) == 0 {
		return input
	}
	merged := make(map[string]any, len(input)+1)
	for key, value := range input {
		merged[key] = value
	}
	patch := ""
	for _, key := range []string{"patch", "input", "_raw"} {
		if value, ok := input[key].(string); ok {
			patch = value
			break
		}
	}
	if patch != "" && !strings.HasSuffix(patch, "\n") {
		patch += "\n"
	}
	paths := make([]string, 0, len(changes))
	for path := range changes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		operation := "Update"
		switch strings.ToLower(changes[path].Type) {
		case "add":
			operation = "Add"
		case "delete":
			operation = "Delete"
		}
		patch += fmt.Sprintf("*** %s File: %s\n", operation, path)
	}
	merged["patch"] = patch
	return merged
}

var exitCodeRe = regexp.MustCompile(`(?im)^(?:Process exited with code|Exit code:)\s*([0-9]+)\s*$`)

func commandOutputFailed(output string) bool {
	trimmed := strings.TrimSpace(output)
	var envelope struct {
		ExitCode *int  `json:"exit_code"`
		TimedOut *bool `json:"timed_out"`
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(trimmed), &envelope) == nil {
		if envelope.ExitCode != nil {
			return *envelope.ExitCode != 0
		}
		if envelope.Metadata.ExitCode != nil {
			return *envelope.Metadata.ExitCode != 0
		}
		if envelope.TimedOut != nil && *envelope.TimedOut {
			return true
		}
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "apply_patch verification failed") {
		return true
	}
	firstLine := trimmed
	if newline := strings.IndexByte(firstLine, '\n'); newline >= 0 {
		firstLine = firstLine[:newline]
	}
	status := strings.ToLower(strings.TrimSpace(firstLine))
	switch {
	case strings.HasPrefix(status, "script completed"), strings.HasPrefix(status, "script running"):
		return false
	case strings.HasPrefix(status, "script failed"):
		return true
	}
	header := trimmed
	for _, marker := range []string{"\nOutput:\n", "\nFinal output:\n"} {
		if index := strings.Index(header, marker); index >= 0 {
			header = header[:index]
		}
	}
	for _, line := range strings.Split(header, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), "aborted by user") {
			return true
		}
	}
	match := exitCodeRe.FindStringSubmatch(header)
	return len(match) == 2 && match[1] != "0"
}

func (a Adapter) titleFor(id string) string {
	if id == "" {
		return ""
	}
	index := a.indexPath()
	if index == "" {
		return ""
	}
	return loadTitleIndex(index)[id]
}

// titleIndexCache holds the parsed session index; summarizing a whole
// sessions directory looks up a title per file, so re-reading the index
// each time would make the scan quadratic
var titleIndexCache struct {
	mu      sync.Mutex
	path    string
	size    int64
	modTime time.Time
	titles  map[string]string
}

func loadTitleIndex(path string) map[string]string {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	titleIndexCache.mu.Lock()
	defer titleIndexCache.mu.Unlock()
	if titleIndexCache.path == path && titleIndexCache.size == info.Size() && titleIndexCache.modTime.Equal(info.ModTime()) {
		return titleIndexCache.titles
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	titles := map[string]string{}
	_ = adapter.ReadJSONLines(f, func(data []byte) {
		var row struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(data, &row) == nil && row.ID != "" && row.ThreadName != "" {
			titles[row.ID] = row.ThreadName
		}
	})
	titleIndexCache.path = path
	titleIndexCache.size = info.Size()
	titleIndexCache.modTime = info.ModTime()
	titleIndexCache.titles = titles
	return titles
}

func (a Adapter) indexPath() string {
	if a.IndexPath != "" {
		return a.IndexPath
	}
	if a.Dir != "" {
		dir := filepath.Clean(a.SessionDir())
		candidate := filepath.Join(filepath.Dir(dir), "session_index.jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if candidate := DefaultIndexPath(); candidate != "" {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	dir := filepath.Clean(a.SessionDir())
	candidate := filepath.Join(filepath.Dir(dir), "session_index.jsonl")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}
