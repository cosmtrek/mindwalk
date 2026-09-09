package codex

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/model"
)

type codexGraphActor = adapter.GraphActor

func (a Adapter) AgentGraphInputs(root model.SessionMeta, catalog []model.SessionMeta) ([]string, error) {
	paths := map[string]bool{root.Path: true}
	childrenByParent := make(map[string][]model.SessionMeta)

	for _, session := range catalog {
		if session.Agent == nil || session.Agent.ParentSessionID == "" || session.Harness != a.Harness() {
			continue
		}

		childrenByParent[session.Agent.ParentSessionID] = append(
			childrenByParent[session.Agent.ParentSessionID],
			session,
		)
	}

	visitedSessions := map[string]bool{root.Key: true}

	queue := []string{root.ID}
	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]

		for _, child := range childrenByParent[parentID] {
			if visitedSessions[child.Key] {
				continue
			}

			visitedSessions[child.Key] = true
			paths[child.Path] = true
			queue = append(queue, codexSessionSourceID(child))
		}
	}

	inputs := make([]string, 0, len(paths))
	for path := range paths {
		if path != "" {
			inputs = append(inputs, path)
		}
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

	childrenByParent := make(map[string][]model.SessionMeta)
	ambiguousRootID := false

	for _, session := range catalog {
		if session.Key != root.Key && session.Harness == a.Harness() && !session.Auxiliary && session.Agent == nil &&
			session.ID == root.ID {
			ambiguousRootID = true
		}

		if session.Agent == nil || session.Agent.ParentSessionID == "" {
			continue
		}

		parentID := session.Agent.ParentSessionID
		childrenByParent[parentID] = append(childrenByParent[parentID], session)
	}

	for parentID := range childrenByParent {
		sort.Slice(childrenByParent[parentID], func(i, j int) bool {
			return childrenByParent[parentID][i].Key < childrenByParent[parentID][j].Key
		})
	}

	visitedSessions := map[string]bool{root.Key: true}
	addedNodes := map[string]bool{mainID: true}

	queue := []codexGraphActor{{Session: root, NodeID: mainID, SourceID: root.ID}}
	for len(queue) > 0 {
		actor := queue[0]
		queue = queue[1:]

		launches, err := readAgentLaunches(actor.Session.Path)
		if err != nil {
			return nil, fmt.Errorf("read agent launches from %s: %w", actor.Session.Path, err)
		}

		children := childrenByParent[actor.SourceID]
		linkedChildren := make(map[string]bool)

		for _, launch := range launches {
			launchOutput, hasIdentity := parseAgentLaunchOutput(launch.Output)
			if hasIdentity && launchOutput.AgentID != "" {
				var child *model.SessionMeta

				for i := range children {
					candidate := &children[i]
					if candidate.Agent.SourceID == launchOutput.AgentID && !visitedSessions[candidate.Key] {
						child = candidate

						break
					}
				}

				node := exactCodexAgentNode(a.Harness(), root.Key, actor, launch, launchOutput, child)
				if !addedNodes[node.ID] {
					graph.Agents = append(graph.Agents, node)
					addedNodes[node.ID] = true
				}

				if child != nil {
					linkedChildren[child.Key] = true
					visitedSessions[child.Key] = true
					queue = append(queue, codexGraphActor{
						Session:  *child,
						NodeID:   node.ID,
						Depth:    node.Depth,
						SourceID: codexSessionSourceID(*child),
					})
				}

				continue
			}

			if hasIdentity && launchOutput.TaskName != "" {
				var child *model.SessionMeta

				for i := range children {
					candidate := &children[i]
					if candidate.Agent.AgentPath == launchOutput.TaskName && !visitedSessions[candidate.Key] {
						child = candidate

						break
					}
				}

				node := legacyCodexAgentNode(a.Harness(), root.Key, actor, launch, launchOutput, child)
				if !addedNodes[node.ID] {
					graph.Agents = append(graph.Agents, node)
					addedNodes[node.ID] = true
				}

				if child != nil {
					linkedChildren[child.Key] = true
					visitedSessions[child.Key] = true
					queue = append(queue, codexGraphActor{
						Session: *child, NodeID: node.ID, Depth: node.Depth, SourceID: codexSessionSourceID(*child),
					})
				}

				continue
			}

			node := unlinkedCodexLaunchNode(a.Harness(), root.Key, actor, launch)
			if !addedNodes[node.ID] {
				graph.Agents = append(graph.Agents, node)
				addedNodes[node.ID] = true
			}
		}

		for i := range children {
			child := children[i]
			if linkedChildren[child.Key] || visitedSessions[child.Key] {
				continue
			}

			if actor.Session.Key == root.Key && ambiguousRootID {
				continue
			}

			node := derivedCodexAgentNode(a.Harness(), root.Key, actor, child)
			if addedNodes[node.ID] {
				continue
			}

			graph.Agents = append(graph.Agents, node)
			addedNodes[node.ID] = true
			visitedSessions[child.Key] = true
			queue = append(queue, codexGraphActor{
				Session:  child,
				NodeID:   node.ID,
				Depth:    node.Depth,
				SourceID: codexSessionSourceID(child),
			})
		}
	}

	graph.Agents = adapter.OrderAgentNodesPreorder(graph.Agents)

	return graph, nil
}

func readAgentLaunches(path string) ([]adapter.AgentLaunch, error) {
	f, closeFile, err := adapter.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer closeFile()

	launches := []adapter.AgentLaunch{}
	launchByCallID := make(map[string]int)
	seenCalls := make(map[string]bool)
	seq := 0
	err = adapter.ReadJSONLines(f, func(data []byte) {
		var line rawLine
		if json.Unmarshal(data, &line) != nil || line.Type != "response_item" {
			return
		}

		var payload responseItemPayload
		if json.Unmarshal(line.Payload, &payload) != nil {
			return
		}

		if call, _, ok := decodeCall(payload, line.Timestamp); ok {
			if seenCalls[call.ID] {
				return
			}

			seenCalls[call.ID] = true
			if call.Name == "spawn_agent" {
				launchByCallID[call.ID] = len(launches)
				launches = append(launches, adapter.AgentLaunch{
					CallID:    call.ID,
					Seq:       seq,
					Arguments: call.Input,
				})
			}

			seq++

			return
		}

		callID, result, ok := decodeOutput(payload)
		if !ok {
			return
		}

		index, ok := launchByCallID[callID]
		if !ok || launches[index].OutputObserved {
			return
		}

		launches[index].Output = result.Content
		launches[index].OutputObserved = true
	})

	return launches, err
}

func exactCodexAgentNode(
	harness, rootKey string,
	actor codexGraphActor,
	launch adapter.AgentLaunch,
	output adapter.AgentLaunchOutput,
	child *model.SessionMeta,
) model.AgentNode {
	seq := launch.Seq

	node := model.AgentNode{
		ID:                 adapter.AgentNodeID(harness, rootKey, "codex-agent:"+output.AgentID),
		ParentID:           actor.NodeID,
		Depth:              actor.Depth + 1,
		Kind:               model.AgentKindSubagent,
		Label:              output.Nickname,
		Role:               agentArgument(launch.Arguments, "agent_type"),
		InstructionPreview: adapter.AgentInstructionPreview(agentArgument(launch.Arguments, "message")),
		LaunchSeq:          &seq,
		LaunchCallID:       launch.CallID,
		Status:             model.AgentStatusLaunched,
		TraceAvailability:  model.TraceAvailabilityMissing,
		LinkQuality:        model.AgentLinkQualityExact,
		LinkMethod:         model.AgentLinkMethodCodexAgentID,
	}
	if child != nil {
		node.Depth = codexChildDepth(*child, actor.Depth)

		node.Label = child.Agent.Label
		if child.Agent.Role != "" {
			node.Role = child.Agent.Role
		}

		node.TraceAvailability = model.TraceAvailabilityAvailable
		node.TraceSessionKey = child.Key
		node.TraceEventCount = child.EventCount
	}

	adapter.ApplyLaunchNickname(&node, output)

	return node
}

func unlinkedCodexLaunchNode(
	harness, rootKey string,
	actor codexGraphActor,
	launch adapter.AgentLaunch,
) model.AgentNode {
	seq := launch.Seq

	return model.AgentNode{
		ID:                 adapter.AgentNodeID(harness, rootKey, "launch:"+actor.NodeID+":"+launch.CallID),
		ParentID:           actor.NodeID,
		Depth:              actor.Depth + 1,
		Kind:               model.AgentKindSubagent,
		Label:              adapter.SubagentLabel,
		Role:               agentArgument(launch.Arguments, "agent_type"),
		InstructionPreview: adapter.AgentInstructionPreview(agentArgument(launch.Arguments, "message")),
		LaunchSeq:          &seq,
		LaunchCallID:       launch.CallID,
		Status:             adapter.UnlinkedLaunchStatus(launch),
		TraceAvailability:  model.TraceAvailabilityUnavailable,
		LinkQuality:        model.AgentLinkQualityUnavailable,
		LinkMethod:         model.AgentLinkMethodUnavailable,
	}
}

func legacyCodexAgentNode(
	harness, rootKey string,
	actor codexGraphActor,
	launch adapter.AgentLaunch,
	output adapter.AgentLaunchOutput,
	child *model.SessionMeta,
) model.AgentNode {
	seq := launch.Seq

	label := filepath.Base(output.TaskName)
	if label == "." || label == "/" || label == "" {
		label = adapter.SubagentLabel
	}

	node := model.AgentNode{
		ID:                 adapter.AgentNodeID(harness, rootKey, "codex-task:"+output.TaskName),
		ParentID:           actor.NodeID,
		Depth:              actor.Depth + 1,
		Kind:               model.AgentKindSubagent,
		Label:              label,
		Role:               agentArgument(launch.Arguments, "agent_type"),
		InstructionPreview: adapter.AgentInstructionPreview(agentArgument(launch.Arguments, "message")),
		LaunchSeq:          &seq,
		LaunchCallID:       launch.CallID,
		Status:             model.AgentStatusLaunched,
		TraceAvailability:  model.TraceAvailabilityMissing,
		LinkQuality:        model.AgentLinkQualityUnavailable,
		LinkMethod:         model.AgentLinkMethodUnavailable,
	}
	if child != nil {
		node.Depth = codexChildDepth(*child, actor.Depth)
		if child.Agent.Label != "" {
			node.Label = child.Agent.Label
		}

		if child.Agent.Role != "" {
			node.Role = child.Agent.Role
		}

		node.TraceAvailability = model.TraceAvailabilityAvailable
		node.TraceSessionKey = child.Key
		node.TraceEventCount = child.EventCount
		node.LinkQuality = model.AgentLinkQualityDerived
		node.LinkMethod = model.AgentLinkMethodCodexParentThreadID
	}

	return node
}

func derivedCodexAgentNode(harness, rootKey string, actor codexGraphActor, child model.SessionMeta) model.AgentNode {
	label := child.Agent.Label
	if label == "" {
		label = adapter.SubagentLabel
	}

	return model.AgentNode{
		ID:                adapter.AgentNodeID(harness, rootKey, "session:"+child.Key),
		ParentID:          actor.NodeID,
		Depth:             codexChildDepth(child, actor.Depth),
		Kind:              model.AgentKindSubagent,
		Label:             label,
		Role:              child.Agent.Role,
		Status:            model.AgentStatusUnknown,
		TraceAvailability: model.TraceAvailabilityAvailable,
		TraceSessionKey:   child.Key,
		TraceEventCount:   child.EventCount,
		LinkQuality:       model.AgentLinkQualityDerived,
		LinkMethod:        model.AgentLinkMethodCodexParentThreadID,
	}
}

func parseAgentLaunchOutput(output string) (adapter.AgentLaunchOutput, bool) {
	var parsed adapter.AgentLaunchOutput
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &parsed) != nil ||
		(parsed.AgentID == "" && parsed.TaskName == "") {
		return adapter.AgentLaunchOutput{}, false
	}

	return parsed, true
}

func agentArgument(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)

	return value
}

func codexChildDepth(child model.SessionMeta, parentDepth int) int {
	if child.Agent.Depth > 0 {
		return child.Agent.Depth
	}

	return parentDepth + 1
}

func codexSessionSourceID(session model.SessionMeta) string {
	if session.Agent != nil && session.Agent.SourceID != "" {
		return session.Agent.SourceID
	}

	return session.ID
}
