package omp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/model"
)

// omp stores a subagent's session at `<parent>/<id>.jsonl`, where <parent>
// is the launching session's file without its extension and <id> is the
// agent id the task tool reported. Nested subagents repeat the layout one
// level down, with ids prefixed by their parent's (`Parent.Child`).

// ompLaunch is one task tool call and the agents it reported starting.
type ompLaunch struct {
	callID  string
	seq     int
	isError bool
	tasks   []launchedTask
}

type launchedTask struct {
	id     string
	agent  string
	prompt string
	status string
}

func (t launchedTask) failed() bool {
	switch t.status {
	case "failed", "error", "aborted", "cancelled":
		return true
	}
	return false
}

type graphActor struct {
	path   string
	nodeID string
	depth  int
}

func artifactDir(sessionPath string) string {
	return strings.TrimSuffix(sessionPath, ".jsonl")
}

func childSessionFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".jsonl" {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

func (a Adapter) AgentGraphInputs(root model.SessionMeta, _ []model.SessionMeta) ([]string, error) {
	inputs := []string{root.Path}
	queue := []string{root.Path}
	for len(queue) > 0 {
		files, err := childSessionFiles(artifactDir(queue[0]))
		queue = queue[1:]
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, files...)
		queue = append(queue, files...)
	}
	sort.Strings(inputs)
	return inputs, nil
}

func (a Adapter) BuildAgentGraph(root model.SessionMeta, catalog []model.SessionMeta) (*model.AgentGraph, error) {
	harness := a.Harness()
	mainID := adapter.AgentNodeID(harness, root.Key, "root:"+root.Key)
	graph := &model.AgentGraph{
		Version:        model.AgentGraphVersion,
		RootSessionKey: root.Key,
		Agents: []model.AgentNode{{
			ID:                mainID,
			Depth:             0,
			Kind:              model.AgentKindMain,
			Label:             "Main",
			Status:            model.AgentStatusMain,
			TraceAvailability: model.TraceAvailabilityAvailable,
			TraceSessionKey:   root.Key,
			TraceEventCount:   root.EventCount,
			LinkQuality:       model.AgentLinkQualityExact,
			LinkMethod:        model.AgentLinkMethodRoot,
		}},
	}

	sessions := map[string]*model.SessionMeta{}
	for i := range catalog {
		if catalog[i].Harness == harness && catalog[i].Agent != nil {
			sessions[filepath.Clean(catalog[i].Path)] = &catalog[i]
		}
	}

	queue := []graphActor{{path: root.Path, nodeID: mainID}}
	for len(queue) > 0 {
		actor := queue[0]
		queue = queue[1:]
		launches, err := a.readLaunches(actor.path)
		if err != nil {
			if actor.nodeID == mainID {
				return nil, err
			}
			launches = nil
		}
		type launchRef struct {
			launch *ompLaunch
			task   launchedTask
		}
		byID := map[string]launchRef{}
		for _, launch := range launches {
			for _, task := range launch.tasks {
				// a retry reuses the name a rejected call requested; the
				// session file belongs to the call that started it
				if existing, seen := byID[task.id]; !seen || (existing.launch.isError && !launch.isError) {
					byID[task.id] = launchRef{launch: launch, task: task}
				}
			}
		}

		files, err := childSessionFiles(artifactDir(actor.path))
		if err != nil {
			return nil, err
		}
		parentID := strings.TrimSuffix(filepath.Base(actor.path), ".jsonl")
		linked := map[string]bool{}
		for _, file := range files {
			id := strings.TrimSuffix(filepath.Base(file), ".jsonl")
			session := sessions[filepath.Clean(file)]
			ref, launched := byID[id]
			// a .jsonl beside the parent that is neither a parsed subagent
			// session nor a reported launch is some other artifact
			if session == nil && !launched {
				continue
			}
			node := model.AgentNode{
				ID:                adapter.AgentNodeID(harness, root.Key, "session:"+adapter.SessionKey(harness, file)),
				ParentID:          actor.nodeID,
				Depth:             actor.depth + 1,
				Kind:              model.AgentKindSubagent,
				Label:             agentLabel(id, parentID),
				Status:            model.AgentStatusLaunched,
				TraceAvailability: model.TraceAvailabilityMissing,
				LinkQuality:       model.AgentLinkQualityDerived,
				LinkMethod:        model.AgentLinkMethodOmpSessionDirectory,
			}
			if launched {
				linked[id] = true
				seq := ref.launch.seq
				node.Role = ref.task.agent
				node.InstructionPreview = adapter.AgentInstructionPreview(ref.task.prompt)
				node.LaunchSeq = &seq
				node.LaunchCallID = ref.launch.callID
				node.LinkQuality = model.AgentLinkQualityExact
				node.LinkMethod = model.AgentLinkMethodOmpTaskID
			}
			if session != nil {
				if session.Agent.Role != "" {
					node.Role = session.Agent.Role
				}
				node.TraceAvailability = model.TraceAvailabilityAvailable
				node.TraceSessionKey = session.Key
				node.TraceEventCount = session.EventCount
				queue = append(queue, graphActor{path: file, nodeID: node.ID, depth: node.Depth})
			}
			graph.Agents = append(graph.Agents, node)
		}

		for _, launch := range launches {
			for _, task := range launch.tasks {
				if linked[task.id] {
					continue
				}
				linked[task.id] = true
				seq := launch.seq
				status := model.AgentStatusUnknown
				if launch.isError || task.failed() {
					status = model.AgentStatusFailed
				}
				label := agentLabel(task.id, parentID)
				if label == "" {
					label = "Subagent"
				}
				graph.Agents = append(graph.Agents, model.AgentNode{
					ID:                 adapter.AgentNodeID(harness, root.Key, "launch:"+actor.nodeID+":"+launch.callID+":"+task.id),
					ParentID:           actor.nodeID,
					Depth:              actor.depth + 1,
					Kind:               model.AgentKindSubagent,
					Label:              label,
					Role:               task.agent,
					InstructionPreview: adapter.AgentInstructionPreview(task.prompt),
					LaunchSeq:          &seq,
					LaunchCallID:       launch.callID,
					Status:             status,
					TraceAvailability:  model.TraceAvailabilityUnavailable,
					LinkQuality:        model.AgentLinkQualityUnavailable,
					LinkMethod:         model.AgentLinkMethodUnavailable,
				})
			}
		}
	}

	graph.Agents = adapter.OrderAgentNodesPreorder(graph.Agents)
	return graph, nil
}

// agentLabel drops the `Parent.` prefix nested agent ids carry; the graph
// already shows the parent.
func agentLabel(id, parentID string) string {
	return strings.TrimPrefix(id, parentID+".")
}

// readLaunches replays a session and collects its task tool calls, each
// with the event seq Parse gives it. The agents a call started come from
// the result details omp records (progress for async launches, results for
// blocking ones); a call whose result reports none — a rejected or failed
// launch — falls back to the task names it requested.
func (a Adapter) readLaunches(path string) ([]*ompLaunch, error) {
	session, err := a.readSession(path)
	if session == nil {
		return nil, err
	}
	var launches []*ompLaunch
	seq := 0
	replay(session.trunk(), func(rawEntry, *ompMessage) {}, func(event toolEvent) {
		if event.call.Name == "task" {
			launch := &ompLaunch{callID: event.call.ID, seq: seq, isError: event.result.IsError}
			launch.tasks = reportedTasks(event.details)
			if len(launch.tasks) == 0 {
				launch.tasks = requestedTasks(event.call.Input)
			}
			launches = append(launches, launch)
		}
		seq++
	})
	return launches, err
}

type taskDetail struct {
	ID         string `json:"id"`
	Agent      string `json:"agent"`
	Assignment string `json:"assignment"`
	Task       string `json:"task"`
	Status     string `json:"status"`
}

func reportedTasks(details json.RawMessage) []launchedTask {
	var parsed struct {
		Progress []taskDetail `json:"progress"`
		Results  []taskDetail `json:"results"`
	}
	if len(details) == 0 || json.Unmarshal(details, &parsed) != nil {
		return nil
	}
	var tasks []launchedTask
	index := map[string]int{}
	for _, detail := range append(parsed.Progress, parsed.Results...) {
		if detail.ID == "" {
			continue
		}
		task := launchedTask{id: detail.ID, agent: detail.Agent, prompt: detail.Assignment, status: detail.Status}
		if task.prompt == "" {
			task.prompt = detail.Task
		}
		// results arrive after progress and carry the final status
		if i, seen := index[detail.ID]; seen {
			if task.status != "" {
				tasks[i].status = task.status
			}
			continue
		}
		index[detail.ID] = len(tasks)
		tasks = append(tasks, task)
	}
	return tasks
}

func requestedTasks(input map[string]any) []launchedTask {
	defaultAgent, _ := input["agent"].(string)
	items, _ := input["tasks"].([]any)
	var tasks []launchedTask
	for _, item := range items {
		spec, _ := item.(map[string]any)
		name, _ := spec["name"].(string)
		if name == "" {
			name, _ = spec["id"].(string)
		}
		if name == "" {
			continue
		}
		agent, _ := spec["agent"].(string)
		if agent == "" {
			agent = defaultAgent
		}
		prompt, _ := spec["task"].(string)
		tasks = append(tasks, launchedTask{id: name, agent: agent, prompt: prompt})
	}
	return tasks
}
