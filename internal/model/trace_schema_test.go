package model

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestTraceSchemaAcceptsOutcomeCertainty(t *testing.T) {
	trace := Trace{
		Version: 1,
		Session: TraceSession{
			ID:         "session",
			Harness:    "codex",
			EventCount: 3,
		},
		Events: []Event{
			{
				Seq:          0,
				Tool:         "exec_command",
				Action:       "verify",
				Targets:      []Target{},
				OutcomeKnown: true,
				Summary:      "run tests",
			},
			{
				Seq:          1,
				Tool:         "exec_command",
				Action:       "verify",
				Targets:      []Target{},
				IsError:      true,
				OutcomeKnown: true,
				Summary:      "tests failed",
			},
			{
				Seq:     2,
				Tool:    "exec_command",
				Action:  "exec",
				Targets: []Target{},
				Summary: "still running",
			},
		},
		Marks: []Mark{},
		Stats: ComputeStats(&Trace{}, 0, ObservabilityEstimated),
	}
	document, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		t.Fatal(err)
	}
	events := value.(map[string]any)["events"].([]any)
	if _, found := events[2].(map[string]any)["outcomeKnown"]; found {
		t.Fatal("unknown outcome serialized outcomeKnown")
	}
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile("../../schema/trace.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("trace with outcome certainty violates schema: %v\n%s", err, document)
	}
}
