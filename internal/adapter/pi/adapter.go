package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/model"
)

type Adapter struct {
	Dir string
}

func DefaultDir() string {
	return adapter.HomePath(".pi", "agent", "sessions")
}

func (a Adapter) Harness() string {
	return "pi"
}

func (a Adapter) SessionDir() string {
	if a.Dir != "" {
		return a.Dir
	}

	return DefaultDir()
}

// sessionIDFromPath derives a stable id for a pi session file. The
// filename without extension is the conventional id; a session header
// that records its own id wins (pi uses a generated id that survives
// resume and rename). Both `Summarize` and `Parse` need the same
// derivation, so the rule lives here.
func sessionIDFromPath(path, headerID string) string {
	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if headerID != "" {
		id = headerID
	}

	return id
}

func (a Adapter) ListSessions() ([]model.SessionMeta, error) {
	dir := a.SessionDir()
	if !adapter.ReadableDir(dir) {
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
	header, entries, recognized, err := readSession(path)
	if err != nil && !recognized {
		return model.SessionMeta{}, err
	}

	if !recognized {
		return model.SessionMeta{}, adapter.NotRecognizedErr(a.Harness(), path)
	}

	id := sessionIDFromPath(path, header.ID)
	meta := model.SessionMeta{
		Key:       adapter.SessionKey(a.Harness(), path),
		ID:        id,
		Harness:   a.Harness(),
		Path:      path,
		Cwd:       header.Cwd,
		StartedAt: header.Timestamp,
		EndedAt:   header.Timestamp,
	}
	// Counting on the same trunk projection Parse plays back keeps
	// EventCount and UserTurns equal to the trace's — the report badge
	// compares the two, and a whole-file count would mark every branched
	// session's fresh report stale.
	firstUserText := ""

	for _, entry := range linearize(entries) {
		if entry.Timestamp != "" {
			if meta.StartedAt == "" {
				meta.StartedAt = entry.Timestamp
			}

			meta.EndedAt = entry.Timestamp
		}

		switch entry.Type {
		case "model_change":
			if entry.ModelID != "" {
				meta.Model = entry.ModelID
			}
		case "message":
			msg, err := decodePiMessage(entry.Message)
			if err != nil {
				continue
			}

			switch msg.Role {
			case "assistant":
				if msg.Model != "" && meta.Model == "" {
					meta.Model = msg.Model
				}

				meta.EventCount += countToolCalls(msg.Content)
			case "user":
				// mirrors Parse's user-message mark filter so the badge's
				// staleness check counts the same turns the report will
				text := contentText(msg.Content)
				if !adapter.InjectedUserMessage(text) {
					meta.UserTurns++

					if firstUserText == "" {
						firstUserText = text
					}
				}
			case "bashExecution":
				meta.EventCount++
			}
		}
	}

	meta.Title = sessionTitle(entries, firstUserText, path)

	return meta, err
}

func (a Adapter) Parse(path string) (*model.Trace, error) {
	header, entries, recognized, err := readSession(path)
	if err != nil && !recognized {
		return nil, err
	}

	if !recognized {
		return nil, adapter.NotRecognizedErr(a.Harness(), path)
	}

	id := sessionIDFromPath(path, header.ID)
	trace := &model.Trace{
		Version: 1,
		Session: model.TraceSession{
			ID:        id,
			Harness:   a.Harness(),
			Path:      path,
			Cwd:       header.Cwd,
			StartedAt: header.Timestamp,
			EndedAt:   header.Timestamp,
		},
		Events: []model.Event{},
		Marks:  []model.Mark{},
	}

	pending := map[string]adapter.ToolCall{}
	pendingOrder := []string{}
	firstUserText := ""

	for _, entry := range linearize(entries) {
		if entry.Timestamp != "" {
			if trace.Session.StartedAt == "" {
				trace.Session.StartedAt = entry.Timestamp
			}

			trace.Session.EndedAt = entry.Timestamp
		}

		switch entry.Type {
		case "model_change":
			// A model_change records a deliberate user switch, so it wins
			// over the per-message model that assistant turns carry.
			if entry.ModelID != "" {
				trace.Session.Model = entry.ModelID
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
		case "custom_message":
			// Extension-injected context. A user-message mark would count
			// it as a user turn in ComputeStats, so it is dropped the same
			// way injected user messages are.
		case "message":
			msg, err := decodePiMessage(entry.Message)
			if err != nil {
				continue
			}

			switch msg.Role {
			case "user":
				text := contentText(msg.Content)
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
			case "assistant":
				if msg.Model != "" && trace.Session.Model == "" {
					trace.Session.Model = msg.Model
				}

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
				isError := msg.IsError != nil && *msg.IsError
				trace.Events = append(trace.Events, adapter.BuildEvent(trace, call, adapter.ToolResult{
					Content:      contentText(msg.Content),
					IsError:      isError,
					OutcomeKnown: msg.IsError != nil,
				}))
			case "bashExecution":
				// A `!` command the user ran from the TUI still touches the
				// repository, so it plays back as a regular bash event.
				call := adapter.ToolCall{
					Name:      "bash",
					Input:     map[string]any{"command": msg.Command},
					Timestamp: entry.Timestamp,
				}
				trace.Events = append(trace.Events, adapter.BuildEvent(trace, call, adapter.ToolResult{
					Content:      msg.Output,
					IsError:      msg.ExitCode != nil && *msg.ExitCode != 0,
					OutcomeKnown: msg.ExitCode != nil,
				}))
			}
		}
	}

	for _, id := range pendingOrder {
		if call, ok := pending[id]; ok {
			trace.Events = append(trace.Events, adapter.BuildEvent(trace, call, adapter.ToolResult{}))
		}
	}

	trace.Session.Title = sessionTitle(entries, firstUserText, path)
	trace.Session.EventCount = len(trace.Events)
	// pi tool results carry an isError flag set by the harness.
	trace.Stats = model.ComputeStats(trace, 0, model.ObservabilitySignals{Errors: model.ObservabilityExact})

	return trace, err
}

// linearize picks the playback path through a pi session tree. The file is
// append-only, so the last entry is the current leaf; walking parentId back
// to the root keeps the trunk the user ended on and drops abandoned
// branches. v1 files carry no entry IDs and are already linear.
func linearize(entries []rawEntry) []rawEntry {
	leaf := -1
	index := map[string]int{}

	for i, entry := range entries {
		if entry.ID != "" {
			leaf = i
			index[entry.ID] = i
		}
	}

	if leaf < 0 {
		return entries
	}

	var path []int

	visited := map[int]bool{}

	cur := leaf
	for !visited[cur] {
		visited[cur] = true
		path = append(path, cur)

		parent := entries[cur].ParentID
		if parent == "" {
			break
		}

		next, ok := index[parent]
		if !ok {
			break
		}

		cur = next
	}

	ordered := make([]rawEntry, 0, len(path))
	for _, v := range slices.Backward(path) {
		ordered = append(ordered, entries[v])
	}

	return ordered
}

type rawEntry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  string          `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	Cwd       string          `json:"cwd"`
	Message   json.RawMessage `json:"message"`
	ModelID   string          `json:"modelId"`
	Summary   string          `json:"summary"`
	Name      string          `json:"name"`
}

type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Model      string          `json:"model"`
	ToolCallID string          `json:"toolCallId"`
	IsError    *bool           `json:"isError"`
	Command    string          `json:"command"`
	Output     string          `json:"output"`
	ExitCode   *int            `json:"exitCode"`
}

func decodePiMessage(data json.RawMessage) (piMessage, error) {
	var msg piMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return piMessage{}, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return piMessage{}, err
	}

	msg.IsError = nil

	if raw, ok := fields["isError"]; ok && strings.TrimSpace(string(raw)) != "null" {
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return piMessage{}, err
		}

		msg.IsError = &value
	}

	msg.ExitCode = nil

	if raw, ok := fields["exitCode"]; ok && strings.TrimSpace(string(raw)) != "null" {
		var value int
		if err := json.Unmarshal(raw, &value); err != nil {
			return piMessage{}, err
		}

		msg.ExitCode = &value
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

// isPiHeader matches pi's own header signature: type "session" plus an id of
// JSON string type — an empty string id is still valid, and legacy sessions
// may lack a cwd, so neither may tighten the check. The raw id bytes stand in
// for JavaScript's `typeof id === "string"`, which a decoded Go string cannot
// express (missing, null, and "" all decode to ""). Claude Code lines never
// use type "session" and Codex wraps its metadata in a "session_meta"
// envelope with no top-level id, so this cannot claim other sources' files.
func isPiHeader(data []byte) bool {
	var probe struct {
		Type string          `json:"type"`
		ID   json.RawMessage `json:"id"`
	}
	if json.Unmarshal(data, &probe) != nil {
		return false
	}

	return probe.Type == "session" && len(probe.ID) > 0 && probe.ID[0] == '"'
}

// readSession loads the header and every tree entry of a pi session file.
// Like pi's loader, blank and malformed lines are skipped without consuming
// the header slot: the first line that parses as JSON must be the header, or
// the file is not a pi session. Entries are only collected once the header
// is accepted.
func readSession(path string) (header rawEntry, entries []rawEntry, recognized bool, err error) {
	f, closeFile, err := adapter.OpenFile(path)
	if err != nil {
		return rawEntry{}, nil, false, err
	}
	defer closeFile()

	sawEntry := false
	err = adapter.ReadJSONLines(f, func(data []byte) {
		var entry rawEntry
		if json.Unmarshal(data, &entry) != nil {
			if !json.Valid(data) {
				return
			}
			// Valid JSON in a shape rawEntry cannot hold still counts as
			// the first parsed entry for pi — and it is not a header.
			sawEntry = true

			return
		}

		if !sawEntry {
			sawEntry = true

			if isPiHeader(data) {
				header = entry
				recognized = true
			}

			return
		}

		if recognized && entry.Type != "" {
			entries = append(entries, entry)
		}
	})

	return header, entries, recognized, err
}

// sessionTitle mirrors pi's getSessionName: the latest session_info entry
// anywhere in the file wins, and an empty name explicitly clears the title.
// Without one, the trunk's first user message names the session, then the
// file name.
func sessionTitle(entries []rawEntry, firstUserText, path string) string {
	for _, v := range slices.Backward(entries) {
		if v.Type != "session_info" {
			continue
		}

		if v.Name != "" {
			return v.Name
		}

		break
	}

	if firstUserText != "" {
		return adapter.AgentInstructionPreview(firstUserText)
	}

	return filepath.Base(path)
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

// countToolCalls counts the calls Parse will turn into events: toolCall
// blocks without an id are skipped there too.
func countToolCalls(raw json.RawMessage) int {
	count := 0

	for _, block := range contentBlocks(raw) {
		if block.Type == "toolCall" && block.ID != "" {
			count++
		}
	}

	return count
}
