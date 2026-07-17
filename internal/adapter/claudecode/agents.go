package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/model"
)

type claudeAgentLaunch struct {
	callID         string
	seq            int
	input          map[string]any
	resultObserved bool
	resultError    bool
	owner          *claudeAgentArtifact
}

type claudeChildSidecar struct {
	Name        string `json:"name"`
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	ToolUseID   string `json:"toolUseId"`
	SpawnDepth  int    `json:"spawnDepth"`
}

type claudeAgentArtifact struct {
	jsonPath string
	session  *model.SessionMeta
	sidecar  *claudeChildSidecar
	nodeID   string
	depth    int
}

func (a Adapter) AgentGraphInputs(root model.SessionMeta, _ []model.SessionMeta) ([]string, error) {
	inputs := []string{root.Path}
	subagentsDir := filepath.Join(filepath.Dir(root.Path), root.ID, "subagents")
	entries, err := os.ReadDir(subagentsDir)
	if os.IsNotExist(err) {
		return inputs, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".jsonl") && !strings.HasSuffix(entry.Name(), ".meta.json")) {
			continue
		}
		inputs = append(inputs, filepath.Join(subagentsDir, entry.Name()))
	}
	sort.Strings(inputs)
	return inputs, nil
}

func (a Adapter) BuildAgentGraph(root model.SessionMeta, catalog []model.SessionMeta) (*model.AgentGraph, error) {
	mainID := adapter.AgentNodeID(a.Harness(), root.Key, "root:"+root.Key)
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

	subagentsDir := filepath.Join(filepath.Dir(root.Path), root.ID, "subagents")
	artifacts, err := discoverClaudeAgentArtifacts(subagentsDir, catalog)
	if err != nil {
		return nil, err
	}

	rootLaunches, err := readClaudeAgentLaunches(root.Path)
	if err != nil {
		return nil, err
	}
	launches := append([]*claudeAgentLaunch(nil), rootLaunches...)
	launchByCallID := make(map[string]*claudeAgentLaunch)
	for _, launch := range rootLaunches {
		launchByCallID[launch.callID] = launch
	}
	for _, artifact := range artifacts {
		if artifact.session == nil {
			continue
		}
		actorLaunches, readErr := readClaudeAgentLaunches(artifact.session.Path)
		if readErr != nil {
			return nil, readErr
		}
		for _, launch := range actorLaunches {
			launch.owner = artifact
			if existing := launchByCallID[launch.callID]; existing != nil {
				if !existing.resultObserved && launch.resultObserved {
					existing.resultObserved = true
					existing.resultError = launch.resultError
				}
				continue
			}
			launchByCallID[launch.callID] = launch
			launches = append(launches, launch)
		}
	}

	for _, artifact := range artifacts {
		identity := "session:" + claudeArtifactSessionKey(a.Harness(), artifact)
		if artifact.sidecar != nil && artifact.sidecar.ToolUseID != "" {
			identity = "claude-tool:" + artifact.sidecar.ToolUseID
		}
		artifact.nodeID = adapter.AgentNodeID(a.Harness(), root.Key, identity)
	}
	for _, artifact := range artifacts {
		resolveClaudeArtifactDepth(artifact, launchByCallID, map[*claudeAgentArtifact]bool{})
	}

	matchedLaunches := make(map[string]bool)
	for _, artifact := range artifacts {
		node, launch := claudeArtifactNode(root.Key, mainID, artifact, launchByCallID)
		graph.Agents = append(graph.Agents, node)
		if launch != nil {
			matchedLaunches[launch.callID] = true
		}
	}
	for _, launch := range launches {
		if matchedLaunches[launch.callID] {
			continue
		}
		graph.Agents = append(graph.Agents, unlinkedClaudeLaunchNode(a.Harness(), root.Key, mainID, launch))
	}

	graph.Agents = adapter.OrderAgentNodesPreorder(graph.Agents)
	return graph, nil
}

func discoverClaudeAgentArtifacts(subagentsDir string, catalog []model.SessionMeta) ([]*claudeAgentArtifact, error) {
	byBasename := make(map[string]*claudeAgentArtifact)
	for i := range catalog {
		session := &catalog[i]
		if filepath.Clean(filepath.Dir(session.Path)) != filepath.Clean(subagentsDir) || filepath.Ext(session.Path) != ".jsonl" {
			continue
		}
		basename := strings.TrimSuffix(filepath.Base(session.Path), ".jsonl")
		byBasename[basename] = &claudeAgentArtifact{jsonPath: session.Path, session: session}
	}

	entries, err := os.ReadDir(subagentsDir)
	if os.IsNotExist(err) {
		return sortedClaudeArtifacts(byBasename), nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		basename := strings.TrimSuffix(entry.Name(), ".meta.json")
		artifact := byBasename[basename]
		if artifact == nil {
			artifact = &claudeAgentArtifact{
				jsonPath: filepath.Join(subagentsDir, basename+".jsonl"),
			}
			byBasename[basename] = artifact
		}
		data, readErr := os.ReadFile(filepath.Join(subagentsDir, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		var sidecar claudeChildSidecar
		if json.Unmarshal(data, &sidecar) == nil {
			artifact.sidecar = &sidecar
		}
	}
	return sortedClaudeArtifacts(byBasename), nil
}

func sortedClaudeArtifacts(byBasename map[string]*claudeAgentArtifact) []*claudeAgentArtifact {
	basenames := make([]string, 0, len(byBasename))
	for basename := range byBasename {
		basenames = append(basenames, basename)
	}
	sort.Strings(basenames)
	artifacts := make([]*claudeAgentArtifact, 0, len(basenames))
	for _, basename := range basenames {
		artifacts = append(artifacts, byBasename[basename])
	}
	return artifacts
}

func readClaudeAgentLaunches(path string) ([]*claudeAgentLaunch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	launches := []*claudeAgentLaunch{}
	launchByCallID := make(map[string]*claudeAgentLaunch)
	seenCalls := make(map[string]bool)
	seq := 0
	err = adapter.ReadJSONLines(f, func(data []byte) {
		var line rawLine
		if json.Unmarshal(data, &line) != nil || len(line.Message) == 0 {
			return
		}
		var msg message
		if json.Unmarshal(line.Message, &msg) != nil {
			return
		}
		for _, item := range msg.Content.Items {
			switch item.Type {
			case "tool_use":
				if seenCalls[item.ID] {
					continue
				}
				seenCalls[item.ID] = true
				if item.Name == "Agent" || item.Name == "Task" {
					launch := &claudeAgentLaunch{callID: item.ID, seq: seq, input: item.Input}
					launches = append(launches, launch)
					launchByCallID[item.ID] = launch
				}
				seq++
			case "tool_result":
				launch := launchByCallID[item.ToolUseID]
				if launch == nil || launch.resultObserved {
					continue
				}
				launch.resultObserved = true
				launch.resultError = item.IsError
			}
		}
	})
	return launches, err
}

func claudeArtifactNode(rootKey, mainID string, artifact *claudeAgentArtifact, launchByCallID map[string]*claudeAgentLaunch) (model.AgentNode, *claudeAgentLaunch) {
	var launch *claudeAgentLaunch
	if artifact.sidecar != nil && artifact.sidecar.ToolUseID != "" {
		launch = launchByCallID[artifact.sidecar.ToolUseID]
	}
	parentID := mainID
	quality := model.AgentLinkQualityDerived
	method := model.AgentLinkMethodClaudeSubagentsDirectory
	if launch != nil && launch.owner != artifact {
		quality = model.AgentLinkQualityExact
		method = model.AgentLinkMethodClaudeToolUseID
		if launch.owner != nil {
			parentID = launch.owner.nodeID
		}
	} else {
		launch = nil
	}

	node := model.AgentNode{
		ID:                artifact.nodeID,
		ParentID:          parentID,
		Depth:             artifact.depth,
		Kind:              model.AgentKindSubagent,
		Label:             claudeArtifactLabel(artifact, launch),
		Role:              claudeArtifactRole(artifact, launch),
		Status:            model.AgentStatusLaunched,
		TraceAvailability: model.TraceAvailabilityMissing,
		LinkQuality:       quality,
		LinkMethod:        method,
	}
	if launch != nil {
		seq := launch.seq
		node.InstructionPreview = adapter.AgentInstructionPreview(claudeLaunchInput(launch, "prompt"))
		node.LaunchSeq = &seq
		node.LaunchCallID = launch.callID
	}
	if artifact.session != nil {
		node.TraceAvailability = model.TraceAvailabilityAvailable
		node.TraceSessionKey = artifact.session.Key
		node.TraceEventCount = artifact.session.EventCount
	}
	return node, launch
}

func unlinkedClaudeLaunchNode(harness, rootKey, mainID string, launch *claudeAgentLaunch) model.AgentNode {
	parentID := mainID
	parentDepth := 0
	if launch.owner != nil {
		parentID = launch.owner.nodeID
		parentDepth = launch.owner.depth
	}
	status := model.AgentStatusUnknown
	if launch.resultObserved && launch.resultError {
		status = model.AgentStatusFailed
	}
	seq := launch.seq
	label := claudeLaunchInput(launch, "description")
	if label == "" {
		label = claudeLaunchInput(launch, "subagent_type")
	}
	if label == "" {
		label = "Subagent"
	}
	return model.AgentNode{
		ID:                 adapter.AgentNodeID(harness, rootKey, "launch:"+parentID+":"+launch.callID),
		ParentID:           parentID,
		Depth:              parentDepth + 1,
		Kind:               model.AgentKindSubagent,
		Label:              label,
		Role:               claudeLaunchInput(launch, "subagent_type"),
		InstructionPreview: adapter.AgentInstructionPreview(claudeLaunchInput(launch, "prompt")),
		LaunchSeq:          &seq,
		LaunchCallID:       launch.callID,
		Status:             status,
		TraceAvailability:  model.TraceAvailabilityUnavailable,
		LinkQuality:        model.AgentLinkQualityUnavailable,
		LinkMethod:         model.AgentLinkMethodUnavailable,
	}
}

func resolveClaudeArtifactDepth(artifact *claudeAgentArtifact, launchByCallID map[string]*claudeAgentLaunch, visiting map[*claudeAgentArtifact]bool) int {
	if artifact.depth > 0 {
		return artifact.depth
	}
	if artifact.sidecar != nil && artifact.sidecar.SpawnDepth > 0 {
		artifact.depth = artifact.sidecar.SpawnDepth
		return artifact.depth
	}
	if visiting[artifact] {
		return 1
	}
	visiting[artifact] = true
	parentDepth := 0
	if artifact.sidecar != nil {
		if launch := launchByCallID[artifact.sidecar.ToolUseID]; launch != nil && launch.owner != nil && launch.owner != artifact {
			parentDepth = resolveClaudeArtifactDepth(launch.owner, launchByCallID, visiting)
		}
	}
	delete(visiting, artifact)
	artifact.depth = parentDepth + 1
	return artifact.depth
}

func claudeArtifactSessionKey(harness string, artifact *claudeAgentArtifact) string {
	if artifact.session != nil {
		return artifact.session.Key
	}
	return adapter.SessionKey(harness, artifact.jsonPath)
}

func claudeArtifactLabel(artifact *claudeAgentArtifact, launch *claudeAgentLaunch) string {
	if artifact.sidecar != nil {
		for _, value := range []string{artifact.sidecar.Name, artifact.sidecar.Description, artifact.sidecar.AgentType} {
			if value != "" {
				return value
			}
		}
	}
	if launch != nil {
		for _, key := range []string{"description", "subagent_type"} {
			if value := claudeLaunchInput(launch, key); value != "" {
				return value
			}
		}
	}
	return "Subagent"
}

func claudeArtifactRole(artifact *claudeAgentArtifact, launch *claudeAgentLaunch) string {
	if artifact.sidecar != nil && artifact.sidecar.AgentType != "" {
		return artifact.sidecar.AgentType
	}
	return claudeLaunchInput(launch, "subagent_type")
}

func claudeLaunchInput(launch *claudeAgentLaunch, key string) string {
	if launch == nil {
		return ""
	}
	value, _ := launch.input[key].(string)
	return value
}
