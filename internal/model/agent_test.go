package model

import (
	"encoding/json"
	"testing"
)

func TestAgentNodeOmitsMainRelationshipFields(t *testing.T) {
	node := AgentNode{
		ID:                "main",
		Depth:             0,
		Kind:              AgentKindMain,
		Label:             "Main",
		Status:            AgentStatusMain,
		TraceAvailability: TraceAvailabilityAvailable,
		TraceSessionKey:   "root-key",
		TraceEventCount:   12,
		LinkQuality:       AgentLinkQualityExact,
		LinkMethod:        AgentLinkMethodRoot,
	}

	data, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["parentId"]; ok {
		t.Fatalf("main node serialized parentId: %s", data)
	}
	if _, ok := got["launchSeq"]; ok {
		t.Fatalf("main node serialized launchSeq: %s", data)
	}
}

func TestAgentNodeSerializesZeroLaunchSeq(t *testing.T) {
	zero := 0
	node := AgentNode{
		ID:                "child",
		ParentID:          "main",
		Depth:             1,
		Kind:              AgentKindSubagent,
		Label:             "Child",
		LaunchSeq:         &zero,
		Status:            AgentStatusLaunched,
		TraceAvailability: TraceAvailabilityMissing,
		LinkQuality:       AgentLinkQualityExact,
		LinkMethod:        AgentLinkMethodCodexAgentID,
	}

	data, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["launchSeq"] != float64(0) {
		t.Fatalf("launchSeq = %#v, JSON = %s", got["launchSeq"], data)
	}
}
