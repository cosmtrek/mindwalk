package model

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestAgentGraphSchemaAcceptsRepresentativeGraph(t *testing.T) {
	launchSeq := 2
	graph := AgentGraph{
		Version:        AgentGraphVersion,
		RootSessionKey: "root-key",
		Agents: []AgentNode{
			{
				ID:                "agt_main",
				Kind:              AgentKindMain,
				Label:             "Main",
				Status:            AgentStatusMain,
				TraceAvailability: TraceAvailabilityAvailable,
				TraceSessionKey:   "root-key",
				TraceEventCount:   12,
				LinkQuality:       AgentLinkQualityExact,
				LinkMethod:        AgentLinkMethodRoot,
			},
			{
				ID:                 "agt_child",
				ParentID:           "agt_main",
				Depth:              1,
				Kind:               AgentKindSubagent,
				Label:              "Reviewer",
				Role:               "reviewer",
				InstructionPreview: "Review the change",
				LaunchSeq:          &launchSeq,
				LaunchCallID:       "call-review",
				Status:             AgentStatusLaunched,
				TraceAvailability:  TraceAvailabilityAvailable,
				TraceSessionKey:    "child-key",
				TraceEventCount:    3,
				LinkQuality:        AgentLinkQualityExact,
				LinkMethod:         AgentLinkMethodCodexAgentID,
			},
			{
				ID:                "agt_missing",
				ParentID:          "agt_main",
				Depth:             1,
				Kind:              AgentKindSubagent,
				Label:             "Missing",
				Status:            AgentStatusLaunched,
				TraceAvailability: TraceAvailabilityMissing,
				LinkQuality:       AgentLinkQualityDerived,
				LinkMethod:        AgentLinkMethodClaudeSubagentsDirectory,
			},
			{
				ID:                "agt_failed",
				ParentID:          "agt_main",
				Depth:             1,
				Kind:              AgentKindSubagent,
				Label:             "Failed",
				Status:            AgentStatusFailed,
				TraceAvailability: TraceAvailabilityUnavailable,
				LinkQuality:       AgentLinkQualityUnavailable,
				LinkMethod:        AgentLinkMethodUnavailable,
			},
		},
	}

	document, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		t.Fatal(err)
	}

	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile("../../schema/agent-graph.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("representative AgentGraph violates schema: %v\n%s", err, document)
	}
}
