package antigravity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain")
}

func (a Adapter) Harness() string {
	return "antigravity"
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
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			transcriptPath := filepath.Join(dir, entry.Name(), ".system_generated", "logs", "transcript.jsonl")
			if info, err := os.Stat(transcriptPath); err == nil && !info.IsDir() {
				meta, err := a.Summarize(transcriptPath)
				if err == nil {
					metas = append(metas, meta)
				}
				continue
			}
			transcriptPath = filepath.Join(dir, entry.Name(), ".system_generated", "logs", "transcript_full.jsonl")
			if info, err := os.Stat(transcriptPath); err == nil && !info.IsDir() {
				meta, err := a.Summarize(transcriptPath)
				if err == nil {
					metas = append(metas, meta)
				}
				continue
			}
		} else if strings.HasSuffix(entry.Name(), ".jsonl") {
			meta, err := a.Summarize(filepath.Join(dir, entry.Name()))
			if err == nil {
				metas = append(metas, meta)
			}
		}
	}
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].EndedAt > metas[j].EndedAt
	})
	return metas, nil
}

func (a Adapter) SummaryInputs(path string) []string {
	return []string{path}
}

// summaryStep is a slim projection used during Summarize to avoid heap allocations
// for CoT thinking, full command contents, diff buffers, or error stacks.
type summaryStep struct {
	StepIndex int              `json:"step_index"`
	Source    string           `json:"source"`
	Type      string           `json:"type"`
	CreatedAt string           `json:"created_at"`
	Content   string           `json:"content"`
	ToolCalls []rawToolSummary `json:"tool_calls"`
}

type rawToolSummary struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

func (a Adapter) Summarize(path string) (model.SessionMeta, error) {
	clean := filepath.Clean(path)
	if strings.Contains(clean, "chunks") {
		return model.SessionMeta{}, fmt.Errorf("ignoring Antigravity chunk: %s", path)
	}
	if filepath.Base(clean) == "transcript_full.jsonl" {
		if _, err := os.Stat(filepath.Join(filepath.Dir(clean), "transcript.jsonl")); err == nil {
			return model.SessionMeta{}, fmt.Errorf("ignoring duplicate transcript_full.jsonl: %s", path)
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return model.SessionMeta{}, err
	}
	defer f.Close()

	id := sessionIDFromPath(path)
	meta := model.SessionMeta{
		Key:     adapter.SessionKey(a.Harness(), path),
		ID:      id,
		Harness: a.Harness(),
		Path:    path,
	}

	recognized := false
	firstUserText := ""
	lastTimestamp := ""

	err = adapter.ReadJSONLines(f, func(data []byte) {
		var step summaryStep
		if json.Unmarshal(data, &step) != nil {
			return
		}
		if isAntigravitySummaryLine(step) {
			recognized = true
		}

		if step.CreatedAt != "" {
			normalizedTS := normalizeTimestamp(step.CreatedAt, lastTimestamp)
			if normalizedTS != "" {
				if meta.StartedAt == "" {
					meta.StartedAt = normalizedTS
				}
				meta.EndedAt = normalizedTS
				lastTimestamp = normalizedTS
			}
		}

		if step.Type == "USER_INPUT" || step.Source == "USER_EXPLICIT" {
			cleanText := cleanUserMessage(step.Content)
			if cleanText != "" && !adapter.InjectedUserMessage(cleanText) {
				meta.UserTurns++
				if firstUserText == "" {
					firstUserText = cleanText
				}
			}
		}

		if len(step.ToolCalls) > 0 {
			meta.EventCount += len(step.ToolCalls)
			if meta.Cwd == "" {
				for _, tc := range step.ToolCalls {
					args := parseArgs(tc.Args)
					if cwd, ok := args["Cwd"].(string); ok && cwd != "" {
						meta.Cwd = cwd
						break
					}
				}
			}
		}
	})

	if meta.Cwd == "" {
		meta.Cwd = discoverWorkspaceCwd(path)
	}

	if firstUserText != "" {
		meta.Title = adapter.AgentInstructionPreview(firstUserText)
	}
	if meta.Title == "" {
		meta.Title = meta.ID
	}
	if meta.Model == "" {
		meta.Model = "gemini"
	}

	if !recognized {
		return model.SessionMeta{}, fmt.Errorf("not an Antigravity session: %s", path)
	}
	return meta, err
}

func (a Adapter) Parse(path string) (*model.Trace, error) {
	clean := filepath.Clean(path)
	if strings.Contains(clean, "chunks") {
		return nil, fmt.Errorf("ignoring Antigravity chunk: %s", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	id := sessionIDFromPath(path)
	cwd := discoverWorkspaceCwd(path)

	trace := &model.Trace{
		Version: 1,
		Session: model.TraceSession{
			ID:      id,
			Harness: a.Harness(),
			Path:    path,
			Cwd:     cwd,
			Model:   "gemini",
		},
		Events: make([]model.Event, 0, 64),
		Marks:  make([]model.Mark, 0, 16),
	}

	recognized := false
	type pendingCall struct {
		call      adapter.ToolCall
		stepIndex int
	}
	var pending []pendingCall
	firstUserText := ""
	lastTimestamp := ""

	err = adapter.ReadJSONLines(f, func(data []byte) {
		var step transcriptStep
		if json.Unmarshal(data, &step) != nil {
			return
		}
		if isAntigravityLine(step) {
			recognized = true
		}

		if step.CreatedAt != "" {
			normalizedTS := normalizeTimestamp(step.CreatedAt, lastTimestamp)
			if normalizedTS != "" {
				if trace.Session.StartedAt == "" {
					trace.Session.StartedAt = normalizedTS
				}
				trace.Session.EndedAt = normalizedTS
				lastTimestamp = normalizedTS
			}
		}

		// Handle Checkpoints / Compaction Marks
		if step.Type == "CHECKPOINT" {
			trace.Marks = append(trace.Marks, model.Mark{
				Seq:  len(trace.Events),
				Type: "compaction",
				Note: "checkpoint",
			})
			return
		}

		// Handle User Input Turns
		if step.Type == "USER_INPUT" || step.Source == "USER_EXPLICIT" {
			cleanText := cleanUserMessage(step.Content)
			if cleanText != "" && !adapter.InjectedUserMessage(cleanText) {
				if firstUserText == "" {
					firstUserText = cleanText
				}
				trace.Marks = append(trace.Marks, model.Mark{
					Seq:  len(trace.Events),
					Type: "user-message",
					Note: adapter.UserMessageNote(cleanText),
				})
			}
			return
		}

		// Handle Model Tool Calls (PLANNER_RESPONSE with tool_calls)
		if len(step.ToolCalls) > 0 {
			ts := step.CreatedAt
			if ts == "" {
				ts = lastTimestamp
			}
			for idx, tc := range step.ToolCalls {
				args := parseArgs(tc.Args)
				if trace.Session.Cwd == "" {
					if cmdCwd, ok := args["Cwd"].(string); ok && cmdCwd != "" {
						trace.Session.Cwd = cmdCwd
					}
				}

				if tc.Name == "invoke_subagent" || tc.Name == "manage_subagents" {
					trace.Marks = append(trace.Marks, model.Mark{
						Seq:  len(trace.Events),
						Type: "subagent",
						Note: tc.Name,
					})
				}

				call := adapter.ToolCall{
					ID:        fmt.Sprintf("call_%d_%d", step.StepIndex, idx),
					Name:      tc.Name,
					Input:     args,
					Timestamp: ts,
				}
				pending = append(pending, pendingCall{call: call, stepIndex: step.StepIndex})
			}
			return
		}

		// Handle Tool Execution Results (GENERIC, ERROR_MESSAGE, or background SYSTEM_MESSAGE)
		if len(pending) > 0 {
			isToolResult := step.Type == "GENERIC" ||
				step.Type == "ERROR_MESSAGE" ||
				(step.Type == "SYSTEM_MESSAGE" && isTaskOrSubagentMessage(step.Content))

			if isToolResult {
				p := pending[0]
				pending[0] = pendingCall{} // Prevent GC memory retention leak
				pending = pending[1:]

				isError := step.Status == "ERROR" || step.Error != "" || step.Type == "ERROR_MESSAGE"
				content := step.Content
				if step.Error != "" && content == "" {
					content = step.Error
				}

				event := adapter.BuildEvent(trace, p.call, adapter.ToolResult{
					Content:      content,
					IsError:      isError,
					OutcomeKnown: true,
				})
				trace.Events = append(trace.Events, event)
			}
		}
	})

	// Flush any pending tool calls that never got a result (aborted / running session)
	for len(pending) > 0 {
		p := pending[0]
		pending[0] = pendingCall{}
		pending = pending[1:]
		event := adapter.BuildEvent(trace, p.call, adapter.ToolResult{
			OutcomeKnown: false,
		})
		trace.Events = append(trace.Events, event)
	}

	for i := range trace.Events {
		trace.Events[i].Seq = i
	}
	trace.Session.EventCount = len(trace.Events)

	if firstUserText != "" {
		trace.Session.Title = adapter.AgentInstructionPreview(firstUserText)
	}
	if trace.Session.Title == "" {
		trace.Session.Title = trace.Session.ID
	}

	trace.Stats = model.ComputeStats(trace, 0, model.ObservabilityExact)
	if !recognized {
		return nil, fmt.Errorf("not an Antigravity session: %s", path)
	}
	return trace, err
}

type transcriptStep struct {
	StepIndex int           `json:"step_index"`
	Source    string        `json:"source"`
	Type      string        `json:"type"`
	Status    string        `json:"status"`
	CreatedAt string        `json:"created_at"`
	Content   string        `json:"content"`
	ToolCalls []rawToolCall `json:"tool_calls"`
	Error     string        `json:"error"`
}

type rawToolCall struct {
	Name        string          `json:"name"`
	Args        json.RawMessage `json:"args"`
	ToolAction  string          `json:"toolAction"`
	ToolSummary string          `json:"toolSummary"`
}

func cleanStringValue(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if unquoted, err := strconv.Unquote(s); err == nil {
			return strings.TrimSpace(unquoted)
		}
		return strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

func sanitizeStringMap(m map[string]any) {
	for k, v := range m {
		if str, ok := v.(string); ok {
			m[k] = cleanStringValue(str)
		}
	}
}

func parseArgs(raw json.RawMessage) map[string]any {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return make(map[string]any)
	}
	var m map[string]any
	if raw[0] == '{' {
		if err := json.Unmarshal(raw, &m); err == nil && m != nil {
			sanitizeStringMap(m)
			return m
		}
	} else if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && s != "" {
			s = strings.TrimSpace(s)
			if strings.HasPrefix(s, "{") {
				if err := json.Unmarshal([]byte(s), &m); err == nil && m != nil {
					sanitizeStringMap(m)
					return m
				}
			}
		}
	}
	return make(map[string]any)
}

func sessionIDFromPath(path string) string {
	clean := filepath.Clean(path)
	parts := strings.Split(clean, string(filepath.Separator))
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == ".system_generated" && i > 0 {
			return parts[i-1]
		}
	}
	base := filepath.Base(clean)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func cleanUserMessage(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// 1. Extract <USER_REQUEST>...</USER_REQUEST> if present
	if strings.Contains(text, "<USER_REQUEST>") {
		start := strings.Index(text, "<USER_REQUEST>") + len("<USER_REQUEST>")
		end := strings.Index(text[start:], "</USER_REQUEST>")
		if end >= 0 {
			return strings.TrimSpace(text[start : start+end])
		}
		// In case of truncated trailing boundary, use remainder
		text = text[start:]
	}

	// 2. Strip XML metadata blocks (<ADDITIONAL_METADATA>, <USER_SETTINGS_CHANGE>, <SYSTEM_MESSAGE>)
	for _, tag := range []string{"ADDITIONAL_METADATA", "USER_SETTINGS_CHANGE", "SYSTEM_MESSAGE"} {
		openTag := "<" + tag + ">"
		closeTag := "</" + tag + ">"
		for {
			start := strings.Index(text, openTag)
			if start < 0 {
				break
			}
			end := strings.Index(text[start:], closeTag)
			if end >= 0 {
				text = strings.TrimSpace(text[:start] + text[start+end+len(closeTag):])
			} else {
				text = strings.TrimSpace(text[:start])
			}
		}
	}
	return strings.TrimSpace(text)
}

func isAntigravitySummaryLine(step summaryStep) bool {
	if step.StepIndex > 0 || (step.StepIndex == 0 && (step.Type != "" || step.Source != "")) {
		switch step.Type {
		case "USER_INPUT", "PLANNER_RESPONSE", "GENERIC", "SYSTEM_MESSAGE", "ERROR_MESSAGE", "CHECKPOINT":
			return true
		}
		switch step.Source {
		case "USER_EXPLICIT", "MODEL", "SYSTEM":
			return true
		}
	}
	for _, tc := range step.ToolCalls {
		switch tc.Name {
		case "view_file", "write_to_file", "replace_file_content", "run_command", "read_url_content", "search_web", "invoke_subagent", "manage_subagents", "ask_question":
			return true
		}
	}
	return false
}

func isAntigravityLine(step transcriptStep) bool {
	if step.StepIndex > 0 || (step.StepIndex == 0 && (step.Type != "" || step.Source != "")) {
		switch step.Type {
		case "USER_INPUT", "PLANNER_RESPONSE", "GENERIC", "SYSTEM_MESSAGE", "ERROR_MESSAGE", "CHECKPOINT":
			return true
		}
		switch step.Source {
		case "USER_EXPLICIT", "MODEL", "SYSTEM":
			return true
		}
	}
	for _, tc := range step.ToolCalls {
		switch tc.Name {
		case "view_file", "write_to_file", "replace_file_content", "run_command", "read_url_content", "search_web", "invoke_subagent", "manage_subagents", "ask_question":
			return true
		}
	}
	return false
}

func isTaskOrSubagentMessage(content string) bool {
	return strings.Contains(content, "Task id ") ||
		strings.Contains(content, "Subagent ") ||
		strings.Contains(content, "finished with result") ||
		strings.Contains(content, "canceled")
}

func normalizeTimestamp(raw string, fallback string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	layouts := []string{
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		time.RFC1123,
		time.DateOnly,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return fallback
}

func parseWorkspaceURI(uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	if u.Scheme != "" && u.Scheme != "file" {
		return ""
	}
	p := u.Path
	if p == "" {
		return ""
	}
	// On Windows, url.Parse("file:///C:/Users/...") gives path="/C:/Users/...".
	// Strip leading slash only if immediately followed by a drive letter.
	if len(p) >= 3 && p[0] == '/' && isDriveLetter(p[1]) && p[2] == ':' {
		p = p[1:]
	}
	cleaned := filepath.Clean(filepath.FromSlash(p))
	return cleaned
}

func isDriveLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func discoverWorkspaceCwd(path string) string {
	clean := filepath.Clean(path)
	var sessionDir string
	parts := strings.Split(clean, string(filepath.Separator))
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == ".system_generated" && i > 0 {
			sessionDir = strings.Join(parts[:i], string(filepath.Separator))
			break
		}
	}

	if sessionDir != "" {
		subagentsDir := filepath.Join(sessionDir, ".system_generated", "subagents")
		if entries, err := os.ReadDir(subagentsDir); err == nil {
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".json") {
					if data, err := os.ReadFile(filepath.Join(subagentsDir, e.Name())); err == nil {
						var meta struct {
							WorkspaceURIs []string `json:"workspaceUris"`
						}
						if json.Unmarshal(data, &meta) == nil && len(meta.WorkspaceURIs) > 0 {
							ws := parseWorkspaceURI(meta.WorkspaceURIs[0])
							if ws != "" {
								if info, err := os.Stat(ws); err == nil && info.IsDir() {
									return ws
								}
								return ws
							}
						}
					}
				}
			}
		}
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return ""
}
