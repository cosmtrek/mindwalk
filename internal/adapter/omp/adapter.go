// Package omp reads oh-my-pi (omp) session logs. omp forked pi's session
// tree format — a "session" header followed by id/parentId entries — and
// layers its own conventions on top: a fixed-width title slot before the
// header, subagent sessions stored in a directory beside their parent's
// file, and a tool vocabulary with its own argument and output dialect.
package omp

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
	return filepath.Join(home, ".omp", "agent", "sessions")
}

func (a Adapter) Harness() string {
	return "omp"
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
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
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
	session, err := a.readSession(path)
	if session == nil {
		return model.SessionMeta{}, err
	}
	meta := model.SessionMeta{
		Key:       adapter.SessionKey(a.Harness(), path),
		ID:        session.id(path),
		Harness:   a.Harness(),
		Path:      path,
		Cwd:       session.header.Cwd,
		StartedAt: session.header.Timestamp,
		EndedAt:   session.header.Timestamp,
	}
	// Counting on the same replay Parse plays back keeps EventCount and
	// UserTurns equal to the trace's — the report badge compares the two.
	var turns userTurns
	firstUserText := ""
	role := ""
	replay(session.trunk(), func(entry rawEntry, msg *ompMessage) {
		if entry.Timestamp != "" {
			if meta.StartedAt == "" {
				meta.StartedAt = entry.Timestamp
			}
			meta.EndedAt = entry.Timestamp
		}
		switch entry.Type {
		case "model_change":
			if m := entry.activeModel(); m != "" {
				meta.Model = m
			}
		case "session_init":
			role = entry.Agent
		case "message":
			if msg.Role == "assistant" && meta.Model == "" {
				meta.Model = msg.model()
			}
			if text, ok := turns.accept(msg); ok {
				meta.UserTurns++
				if firstUserText == "" {
					firstUserText = text
				}
			}
		}
	}, func(toolEvent) {
		meta.EventCount++
	})
	meta.Title = session.title(firstUserText, path)
	if session.subagent(path) {
		// Subagents play back from their parent's agent graph; keeping them
		// auxiliary stops each one from crowding the session list.
		meta.Auxiliary = true
		meta.Agent = &model.AgentSessionMeta{
			SourceID: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
			Role:     role,
		}
	}
	return meta, err
}

func (a Adapter) Parse(path string) (*model.Trace, error) {
	session, err := a.readSession(path)
	if session == nil {
		return nil, err
	}
	trace := &model.Trace{
		Version: 1,
		Session: model.TraceSession{
			ID:        session.id(path),
			Harness:   a.Harness(),
			Path:      path,
			Cwd:       session.header.Cwd,
			StartedAt: session.header.Timestamp,
			EndedAt:   session.header.Timestamp,
		},
		Events: []model.Event{},
		Marks:  []model.Mark{},
	}

	var turns userTurns
	firstUserText := ""
	replay(session.trunk(), func(entry rawEntry, msg *ompMessage) {
		if entry.Timestamp != "" {
			if trace.Session.StartedAt == "" {
				trace.Session.StartedAt = entry.Timestamp
			}
			trace.Session.EndedAt = entry.Timestamp
		}
		switch entry.Type {
		case "model_change":
			// A model_change records a deliberate switch, so it wins over
			// the per-message model that assistant turns carry.
			if m := entry.activeModel(); m != "" {
				trace.Session.Model = m
			}
		case "compaction":
			trace.Marks = append(trace.Marks, model.Mark{Seq: len(trace.Events), Type: "compaction"})
		case "branch_summary":
			// Switching branches folds the abandoned path into a summary,
			// which reads like a compaction on the surviving trunk.
			trace.Marks = append(trace.Marks, model.Mark{
				Seq:  len(trace.Events),
				Type: "compaction",
				Note: adapter.UserMessageNote("branch: " + entry.Summary),
			})
		case "message":
			switch msg.Role {
			case "user":
				if text, ok := turns.accept(msg); ok {
					trace.Marks = append(trace.Marks, model.Mark{
						Seq:  len(trace.Events),
						Type: "user-message",
						Note: adapter.UserMessageNote(text),
					})
					if firstUserText == "" {
						firstUserText = text
					}
				}
			case "assistant":
				if trace.Session.Model == "" {
					trace.Session.Model = msg.model()
				}
				for _, block := range contentBlocks(msg.Content) {
					if block.Type == "toolCall" && block.ID != "" && block.Name == "task" {
						trace.Marks = append(trace.Marks, model.Mark{Seq: len(trace.Events), Type: "subagent", Note: block.Name})
					}
				}
			}
		}
	}, func(event toolEvent) {
		call := event.call
		call.Resolved = resolveCall(call, event.result.Content)
		trace.Events = append(trace.Events, adapter.BuildEvent(trace, call, event.result))
	})
	trace.Session.Title = session.title(firstUserText, path)
	trace.Session.EventCount = len(trace.Events)
	// omp tool results carry an isError flag set by the harness.
	trace.Stats = model.ComputeStats(trace, 0, model.ObservabilityExact)
	return trace, err
}

// userTurns decides which user-role messages are user turns. A session's
// first user message is its task whoever sent it — a subagent's assignment
// arrives agent-attributed. Later agent-attributed messages are relayed
// harness traffic (parent IRC, budget notices, async job results) and are
// dropped the same way injected markup is.
type userTurns struct {
	seen bool
}

func (u *userTurns) accept(msg *ompMessage) (string, bool) {
	if msg.Role != "user" {
		return "", false
	}
	text := contentText(msg.Content)
	if adapter.InjectedUserMessage(text) {
		return "", false
	}
	if msg.Attribution == "agent" && u.seen {
		return "", false
	}
	u.seen = true
	return text, true
}

type toolEvent struct {
	call    adapter.ToolCall
	result  adapter.ToolResult
	details json.RawMessage
}

// replay walks a trunk in playback order. Parse, Summarize, and the agent
// graph's launch scan all replay the same way, so a count or launch seq
// taken from one matches the events Parse emits. visitEntry sees every
// trunk entry (msg is nil unless the entry is a message) before the events
// that entry produces; visitEvent receives events in trace order.
func replay(entries []rawEntry, visitEntry func(rawEntry, *ompMessage), visitEvent func(toolEvent)) {
	pending := map[string]adapter.ToolCall{}
	pendingOrder := []string{}
	for _, entry := range entries {
		if entry.Type != "message" {
			visitEntry(entry, nil)
			continue
		}
		msg, err := decodeMessage(entry.Message)
		if err != nil {
			continue
		}
		visitEntry(entry, &msg)
		switch msg.Role {
		case "assistant":
			for _, block := range contentBlocks(msg.Content) {
				if block.Type != "toolCall" || block.ID == "" {
					continue
				}
				call := adapter.ToolCall{
					ID:        block.ID,
					Name:      block.Name,
					Input:     block.Arguments,
					Timestamp: entry.Timestamp,
				}
				if _, exists := pending[call.ID]; !exists {
					pendingOrder = append(pendingOrder, call.ID)
				}
				pending[call.ID] = call
			}
		case "toolResult":
			call, ok := pending[msg.ToolCallID]
			if !ok {
				continue
			}
			delete(pending, msg.ToolCallID)
			visitEvent(toolEvent{
				call: call,
				result: adapter.ToolResult{
					Content:      contentText(msg.Content),
					IsError:      msg.IsError != nil && *msg.IsError,
					OutcomeKnown: msg.IsError != nil,
				},
				details: msg.Details,
			})
		case "bashExecution", "pythonExecution":
			// A `!` or `$` command the user ran from the TUI still touches
			// the repository, so it plays back as a regular tool event.
			call := adapter.ToolCall{
				Name:      "bash",
				Input:     map[string]any{"command": msg.Command},
				Timestamp: entry.Timestamp,
			}
			if msg.Role == "pythonExecution" {
				call.Name = "eval"
				call.Input = map[string]any{"code": msg.Code, "language": "python"}
			}
			visitEvent(toolEvent{call: call, result: adapter.ToolResult{
				Content:      msg.Output,
				IsError:      msg.ExitCode != nil && *msg.ExitCode != 0,
				OutcomeKnown: msg.ExitCode != nil,
			}})
		}
	}
	for _, id := range pendingOrder {
		if call, ok := pending[id]; ok {
			visitEvent(toolEvent{call: call})
		}
	}
}

type session struct {
	header    rawEntry
	slotTitle string
	entries   []rawEntry
}

func (s *session) id(path string) string {
	if s.header.ID != "" {
		return s.header.ID
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// title mirrors omp's precedence: the title slot carries the current title,
// title_change entries record each rename, and the header keeps the title
// the session was created with. Without any, the first user turn names the
// session, then the file name.
func (s *session) title(firstUserText, path string) string {
	if s.slotTitle != "" {
		return s.slotTitle
	}
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].Type == "title_change" && s.entries[i].Title != "" {
			return s.entries[i].Title
		}
	}
	if s.header.Title != "" {
		return s.header.Title
	}
	if firstUserText != "" {
		return adapter.AgentInstructionPreview(firstUserText)
	}
	return filepath.Base(path)
}

// subagent reports whether the file is a task subagent's session. omp writes
// those into the artifact directory named after the parent's file and points
// parentSession at that file. Matching on names rather than the absolute
// parentSession path keeps the check intact when the store is relocated, and
// leaves forks — which also carry parentSession but sit beside their source —
// as top-level sessions.
func (s *session) subagent(path string) bool {
	parent := s.header.ParentSession
	if parent == "" {
		return false
	}
	return filepath.Base(filepath.Dir(path)) == strings.TrimSuffix(filepath.Base(parent), ".jsonl")
}

// trunk picks the playback path through the session tree the way omp's
// resolveSessionLeafId does: the last entry is the leaf, except that a
// late model_usage entry restores the leaf it captured (null meaning the
// session was cleared). A captured leaf that no longer exists falls back to
// the newest self-selecting entry. Walking parentId from the leaf keeps the
// branch the user ended on and drops abandoned ones; entries without IDs
// are already linear.
func (s *session) trunk() []rawEntry {
	entries := s.entries
	index := map[string]int{}
	for i, entry := range entries {
		if entry.ID != "" {
			index[entry.ID] = i
		}
	}
	if len(index) == 0 {
		return entries
	}
	last := entries[len(entries)-1]
	leafID, selfSelected := last.leafID()
	if !selfSelected {
		if leafID == "" {
			return nil
		}
		if _, ok := index[leafID]; !ok {
			leafID = ""
			for i := len(entries) - 1; i >= 0; i-- {
				if id, self := entries[i].leafID(); self && id != "" {
					leafID = id
					break
				}
			}
		}
	}
	leaf, ok := index[leafID]
	if !ok {
		return entries
	}
	var path []int
	visited := map[int]bool{}
	cur := leaf
	for !visited[cur] {
		visited[cur] = true
		path = append(path, cur)
		next, ok := index[entries[cur].ParentID]
		if entries[cur].ParentID == "" || !ok {
			break
		}
		cur = next
	}
	ordered := make([]rawEntry, 0, len(path))
	for i := len(path) - 1; i >= 0; i-- {
		ordered = append(ordered, entries[path[i]])
	}
	return ordered
}

type rawEntry struct {
	Type          string          `json:"type"`
	ID            string          `json:"id"`
	ParentID      string          `json:"parentId"`
	Timestamp     string          `json:"timestamp"`
	Cwd           string          `json:"cwd"`
	Title         string          `json:"title"`
	ParentSession string          `json:"parentSession"`
	Message       json.RawMessage `json:"message"`
	Model         string          `json:"model"`
	Role          string          `json:"role"`
	Summary       string          `json:"summary"`
	Agent         string          `json:"agent"`
	ActiveLeafID  json.RawMessage `json:"activeLeafId"`
}

// leafID is omp's sessionEntryLeafId: the entry selects itself unless it is
// a model_usage entry carrying activeLeafId, whose value (possibly null,
// reported as "") is the leaf instead.
func (e rawEntry) leafID() (string, bool) {
	if e.Type != "model_usage" || len(e.ActiveLeafID) == 0 {
		return e.ID, true
	}
	var id string
	if json.Unmarshal(e.ActiveLeafID, &id) != nil {
		return "", false
	}
	return id, false
}

// activeModel returns the conversation model a model_change selects. Changes
// for other roles (smol, slow, …) and ephemeral retry fallbacks do not
// switch the model the session runs on.
func (e rawEntry) activeModel() string {
	if e.Role != "" && e.Role != "default" {
		return ""
	}
	return e.Model
}

type ompMessage struct {
	Role        string          `json:"role"`
	Content     json.RawMessage `json:"content"`
	Provider    string          `json:"provider"`
	Model       string          `json:"model"`
	Attribution string          `json:"attribution"`
	ToolCallID  string          `json:"toolCallId"`
	Details     json.RawMessage `json:"details"`
	IsError     *bool           `json:"isError"`
	Command     string          `json:"command"`
	Code        string          `json:"code"`
	Output      string          `json:"output"`
	ExitCode    *int            `json:"exitCode"`
}

// model formats an assistant turn's model like model_change does, as
// "provider/model", so sessions read the same with or without a change entry.
func (m *ompMessage) model() string {
	if m.Model == "" || m.Provider == "" {
		return m.Model
	}
	return m.Provider + "/" + m.Model
}

// decodeMessage keeps isError and exitCode nil unless the exact key is
// present with a non-null value: encoding/json matches keys
// case-insensitively, and a stray "IsError" must not pass for a recorded
// outcome.
func decodeMessage(data json.RawMessage) (ompMessage, error) {
	var msg ompMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return ompMessage{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return ompMessage{}, err
	}
	if _, ok := fields["isError"]; !ok {
		msg.IsError = nil
	}
	if _, ok := fields["exitCode"]; !ok {
		msg.ExitCode = nil
	}
	return msg, nil
}

type contentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func contentBlocks(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		return blocks
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return []contentBlock{{Type: "text", Text: s}}
	}
	return nil
}

func contentText(raw json.RawMessage) string {
	var parts []string
	for _, block := range contentBlocks(raw) {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n")
}

// isTitleSlot matches omp's parseTitleSlotObject: the fixed-width first line
// holding the mutable current title.
func isTitleSlot(data []byte) (string, bool) {
	var slot struct {
		Type      string  `json:"type"`
		V         int     `json:"v"`
		Title     *string `json:"title"`
		UpdatedAt *string `json:"updatedAt"`
		Pad       *string `json:"pad"`
	}
	if json.Unmarshal(data, &slot) != nil || slot.Type != "title" || slot.V != 1 {
		return "", false
	}
	if slot.Title == nil || slot.UpdatedAt == nil || slot.Pad == nil {
		return "", false
	}
	return *slot.Title, true
}

// isHeader matches the pi-family header signature: type "session" plus an id
// of JSON string type.
func isHeader(data []byte) bool {
	var probe struct {
		Type string          `json:"type"`
		ID   json.RawMessage `json:"id"`
	}
	if json.Unmarshal(data, &probe) != nil {
		return false
	}
	return probe.Type == "session" && len(probe.ID) > 0 && probe.ID[0] == '"'
}

// readSession loads an omp session file. Like omp's loader, blank and
// malformed lines are skipped, an optional title slot may precede the
// header, and the first other parsed line must be the header. A file with a
// title slot is unambiguously omp's; a legacy slot-less file has the same
// shape as a pi session, so it is claimed only from omp's own session store.
// It returns a nil session when the file is not an omp session.
func (a Adapter) readSession(path string) (*session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := &session{}
	hasSlot, decided, recognized := false, false, false
	err = adapter.ReadJSONLines(f, func(data []byte) {
		if !json.Valid(data) {
			return
		}
		if !decided {
			if title, ok := isTitleSlot(data); ok && !hasSlot {
				hasSlot = true
				s.slotTitle = title
				return
			}
			decided = true
			if isHeader(data) && json.Unmarshal(data, &s.header) == nil {
				recognized = hasSlot || a.inStore(path)
			}
			return
		}
		if !recognized {
			return
		}
		var entry rawEntry
		if json.Unmarshal(data, &entry) == nil && entry.Type != "" {
			s.entries = append(s.entries, entry)
		}
	})
	if !recognized {
		if err == nil {
			err = fmt.Errorf("not an omp session: %s", path)
		}
		return nil, err
	}
	return s, err
}

func (a Adapter) inStore(path string) bool {
	dir := a.SessionDir()
	if dir == "" {
		return false
	}
	absDir, err1 := filepath.Abs(dir)
	absPath, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}
