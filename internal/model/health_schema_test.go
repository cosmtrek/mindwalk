package model

import (
	"encoding/json"
	"os"
	"sort"
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

func TestSessionHealthSchemaAcceptsEverySignalState(t *testing.T) {
	tests := []struct {
		name         string
		availability string
		quality      string
		omitQuality  bool
	}{
		{name: "ready exact", availability: HealthReady, quality: ObservabilityExact},
		{name: "ready estimated", availability: HealthReady, quality: ObservabilityEstimated},
		{name: "ready unavailable", availability: HealthReady, quality: ObservabilityUnavailable},
		{name: "not applicable", availability: HealthNotApplicable, omitQuality: true},
		{name: "failed", availability: HealthFailed, omitQuality: true},
	}
	schema := compileHealthSchema(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := representativeHealth(tt.availability, tt.quality)
			assertSchemaAccepts(t, schema, health)
			if !tt.omitQuality {
				return
			}
			document, err := json.Marshal(health)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(document, &payload); err != nil {
				t.Fatal(err)
			}
			for name, value := range payload["signals"].(map[string]any) {
				if _, found := value.(map[string]any)["quality"]; found {
					t.Fatalf("%s signal serialized quality for %s", name, tt.name)
				}
			}
		})
	}
}

func TestBrowserHealthFixturesConformToSessionHealthSchema(t *testing.T) {
	document, err := os.ReadFile("../../testdata/agent-lens/browser-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtureFile struct {
		Health map[string]json.RawMessage `json:"health"`
	}
	if err := json.Unmarshal(document, &fixtureFile); err != nil {
		t.Fatal(err)
	}
	if len(fixtureFile.Health) == 0 {
		t.Fatal("browser fixture file contains no health fixtures")
	}
	names := make([]string, 0, len(fixtureFile.Health))
	for name := range fixtureFile.Health {
		names = append(names, name)
	}
	sort.Strings(names)
	schema := compileHealthSchema(t)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			var payload any
			if err := json.Unmarshal(fixtureFile.Health[name], &payload); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(payload); err != nil {
				t.Fatalf("schema rejected browser health fixture %q: %v", name, err)
			}
		})
	}
}

func representativeHealth(availability, quality string) SessionHealth {
	signal := func() HealthSignal {
		return HealthSignal{Availability: availability, Quality: quality, Reason: "representative", Affects: []string{}}
	}
	return SessionHealth{
		Version:    SessionHealthVersion,
		SessionKey: "representative",
		Signals: HealthSignals{
			Reads:        ReadHealth{HealthSignal: signal()},
			Errors:       ErrorHealth{HealthSignal: signal()},
			Verification: VerificationHealth{HealthSignal: signal()},
			Subagents:    SubagentHealth{HealthSignal: signal()},
		},
	}
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
