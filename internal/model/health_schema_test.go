package model

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compileHealthSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile("../../schema/session-health.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func compileTraceSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile("../../schema/trace.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func assertSchemaAccepts(t *testing.T, schema *jsonschema.Schema, value any) {
	t.Helper()
	document, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(decoded); err != nil {
		t.Fatalf("schema rejected payload: %v\n%s", err, document)
	}
}

func TestSessionHealthSchemaAcceptsRepresentativeHealth(t *testing.T) {
	health := SessionHealth{
		Version:    SessionHealthVersion,
		SessionKey: "codex-root",
		Signals: HealthSignals{
			Reads:        ReadHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonReadsInferred, Affects: []string{"read-coverage"}}, DirectCount: 18, InferredCount: 12},
			Errors:       ErrorHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityExact, Reason: HealthReasonStructuredErrors, Affects: []string{}}},
			Verification: VerificationHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonVerificationUnknown, Affects: []string{"verification-result"}}, RecognizedCount: 4, KnownResultCount: 3, UnknownResultCount: 1},
			Subagents:    SubagentHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonMixedAgentLinks, Affects: []string{"subagent-trace"}}, ExactCount: 3, DerivedCount: 1, MissingTraceCount: 1},
		},
	}
	assertSchemaAccepts(t, compileHealthSchema(t), health)
}

func TestSessionHealthSchemaRejectsReadySignalWithoutQuality(t *testing.T) {
	health := SessionHealth{
		Version:    SessionHealthVersion,
		SessionKey: "codex-root",
		Signals: HealthSignals{
			Reads:        ReadHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonReadsInferred, Affects: []string{}}},
			Errors:       ErrorHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityExact, Reason: HealthReasonStructuredErrors, Affects: []string{}}},
			Verification: VerificationHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonVerificationUnknown, Affects: []string{}}},
			Subagents:    SubagentHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonMixedAgentLinks, Affects: []string{}}},
		},
	}
	document, err := json.Marshal(health)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(document, &payload); err != nil {
		t.Fatal(err)
	}
	signals := payload["signals"].(map[string]any)
	reads := signals["reads"].(map[string]any)
	delete(reads, "quality")
	if err := compileHealthSchema(t).Validate(payload); err == nil {
		t.Fatal("schema accepted ready reads signal without quality")
	}
}

func TestSessionHealthSchemaRejectsNullAffects(t *testing.T) {
	health := SessionHealth{
		Version:    SessionHealthVersion,
		SessionKey: "codex-root",
		Signals: HealthSignals{
			Reads:        ReadHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonReadsInferred, Affects: []string{}}},
			Errors:       ErrorHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityExact, Reason: HealthReasonStructuredErrors, Affects: []string{}}},
			Verification: VerificationHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonVerificationUnknown, Affects: []string{}}},
			Subagents:    SubagentHealth{HealthSignal: HealthSignal{Availability: HealthReady, Quality: ObservabilityEstimated, Reason: HealthReasonMixedAgentLinks, Affects: []string{}}},
		},
	}
	document, err := json.Marshal(health)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(document, &payload); err != nil {
		t.Fatal(err)
	}
	signals := payload["signals"].(map[string]any)
	signals["reads"].(map[string]any)["affects"] = nil
	if err := compileHealthSchema(t).Validate(payload); err == nil {
		t.Fatal("schema accepted null affects")
	}
}

func TestTraceHealthEvidenceIsNotSerialized(t *testing.T) {
	trace := Trace{
		Version: 1,
		Session: TraceSession{
			ID:         "codex-root",
			Harness:    "codex",
			EventCount: 0,
		},
		Events: []Event{},
		Marks:  []Mark{},
		Stats: Stats{
			Actions:       ActionCounts{},
			Errors:        ActionCounts{},
			Observability: Observability{Reads: ObservabilityExact, Errors: ObservabilityExact},
		},
		HealthEvidence: TraceHealthEvidence{
			Verification: VerificationEvidence{
				RecognizedCount:    1,
				KnownResultCount:   1,
				UnknownResultCount: 1,
			},
		},
	}
	document, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(document, &payload); err != nil {
		t.Fatal(err)
	}
	if _, found := payload["healthEvidence"]; found {
		t.Fatal("trace JSON included healthEvidence")
	}
	if _, found := payload["verification"]; found {
		t.Fatal("trace JSON included verification evidence")
	}
	if err := compileTraceSchema(t).Validate(payload); err != nil {
		t.Fatalf("trace with in-memory evidence violates v1 schema: %v", err)
	}
}
