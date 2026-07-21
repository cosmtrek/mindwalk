package health

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestBuildReadSignals(t *testing.T) {
	tests := []struct {
		name  string
		trace *model.Trace
		want  model.ReadHealth
	}{
		{
			name:  "exact direct targets",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact),
			want:  readHealth(model.HealthReady, model.ObservabilityExact, model.HealthReasonStructuredReads, 2, 0),
		},
		{
			name:  "estimated weak target",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withWeakRead()),
			want:  readHealth(model.HealthReady, model.ObservabilityEstimated, model.HealthReasonReadsInferred, 2, 1),
		},
		{
			name:  "estimated adapter signal",
			trace: traceWith(model.ObservabilityEstimated, model.ObservabilityExact),
			want:  readHealth(model.HealthReady, model.ObservabilityEstimated, model.HealthReasonReadsInferred, 2, 0),
		},
		{
			name:  "unavailable",
			trace: traceWith(model.ObservabilityUnavailable, model.ObservabilityExact),
			want:  readHealth(model.HealthReady, model.ObservabilityUnavailable, model.HealthReasonReadsUnavailable, 2, 0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Build("root", tt.trace, nil, nil).Signals.Reads; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("reads = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildErrorSignals(t *testing.T) {
	tests := []struct {
		name  string
		trace *model.Trace
		want  model.ErrorHealth
	}{
		{
			name:  "exact",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact),
			want:  errorHealth(model.HealthReady, model.ObservabilityExact, model.HealthReasonStructuredErrors, 3),
		},
		{
			name:  "estimated with no recognized errors",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityEstimated, withErrors(0)),
			want:  errorHealth(model.HealthReady, model.ObservabilityEstimated, model.HealthReasonErrorsInferred, 0),
		},
		{
			name:  "unavailable",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityUnavailable),
			want:  errorHealth(model.HealthReady, model.ObservabilityUnavailable, model.HealthReasonErrorsUnavailable, 3),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Build("root", tt.trace, nil, nil).Signals.Errors; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("errors = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildVerificationSignals(t *testing.T) {
	tests := []struct {
		name  string
		trace *model.Trace
		want  model.VerificationHealth
	}{
		{
			name:  "exact recognition and known outcomes",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withVerification(model.ObservabilityExact, 2, 2, 0, 0)),
			want:  verificationHealth(model.HealthReady, model.ObservabilityExact, model.HealthReasonStructuredVerify, 2, 2, 0, 0),
		},
		{
			name:  "estimated recognition with known outcomes",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withVerification(model.ObservabilityEstimated, 2, 2, 0, 0)),
			want:  verificationHealth(model.HealthReady, model.ObservabilityEstimated, model.HealthReasonVerificationInferred, 2, 2, 0, 0),
		},
		{
			name:  "unknown outcome",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withVerification(model.ObservabilityExact, 3, 2, 1, 4)),
			want:  verificationHealth(model.HealthReady, model.ObservabilityEstimated, model.HealthReasonVerificationUnknown, 3, 2, 1, 4),
		},
		{
			name:  "no usable signal",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withMissingVerificationEvidence(2)),
			want:  verificationHealth(model.HealthReady, model.ObservabilityUnavailable, model.HealthReasonVerificationUnavailable, 2, 0, 0, 0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Build("root", tt.trace, nil, nil).Signals.Verification; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("verification = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildSubagentSignals(t *testing.T) {
	tests := []struct {
		name     string
		trace    *model.Trace
		graph    *model.AgentGraph
		graphErr error
		want     model.SubagentHealth
	}{
		{
			name:  "not applicable without launches",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withSubagents(0)),
			want:  subagentHealth(model.HealthNotApplicable, "", model.HealthReasonNoSubagents, 0, 0, 0, 0),
		},
		{
			name:     "failed graph load",
			trace:    traceWith(model.ObservabilityExact, model.ObservabilityExact, withSubagents(1)),
			graphErr: errors.New("graph failed"),
			want:     subagentHealth(model.HealthFailed, "", model.HealthReasonAgentGraphFailed, 0, 0, 0, 0),
		},
		{
			name:  "unavailable graph context",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withSubagents(1)),
			want:  subagentHealth(model.HealthReady, model.ObservabilityUnavailable, model.HealthReasonAgentContextMissing, 0, 0, 0, 0),
		},
		{
			name:  "estimated when recorded launch has no graph child",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withSubagents(1)),
			graph: graphWith(),
			want:  subagentHealth(model.HealthReady, model.ObservabilityEstimated, model.HealthReasonMixedAgentLinks, 0, 0, 0, 0),
		},
		{
			name:  "estimated unavailable link with available trace",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withSubagents(1)),
			graph: graphWith(agent(model.AgentLinkQualityUnavailable, model.TraceAvailabilityAvailable)),
			want:  subagentHealth(model.HealthReady, model.ObservabilityEstimated, model.HealthReasonMixedAgentLinks, 0, 0, 0, 0),
		},
		{
			name:  "exact children",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withSubagents(2)),
			graph: graphWith(agent(model.AgentLinkQualityExact, model.TraceAvailabilityAvailable), agent(model.AgentLinkQualityExact, model.TraceAvailabilityAvailable)),
			want:  subagentHealth(model.HealthReady, model.ObservabilityExact, model.HealthReasonExactAgentLinks, 2, 0, 0, 0),
		},
		{
			name:  "exact nested descendants compare root launches with direct children",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withSubagents(1)),
			graph: graphWith(
				model.AgentNode{ID: "child", ParentID: "main", Kind: model.AgentKindSubagent, LinkQuality: model.AgentLinkQualityExact, TraceAvailability: model.TraceAvailabilityAvailable},
				model.AgentNode{ID: "grandchild", ParentID: "child", Kind: model.AgentKindSubagent, LinkQuality: model.AgentLinkQualityExact, TraceAvailability: model.TraceAvailabilityAvailable},
			),
			want: subagentHealth(model.HealthReady, model.ObservabilityExact, model.HealthReasonExactAgentLinks, 2, 0, 0, 0),
		},
		{
			name:  "estimated mixed children",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withSubagents(4)),
			graph: graphWith(
				agent(model.AgentLinkQualityExact, model.TraceAvailabilityAvailable),
				agent(model.AgentLinkQualityDerived, model.TraceAvailabilityAvailable),
				agent(model.AgentLinkQualityExact, model.TraceAvailabilityMissing),
				agent(model.AgentLinkQualityUnavailable, model.TraceAvailabilityUnavailable),
			),
			want: subagentHealth(model.HealthReady, model.ObservabilityEstimated, model.HealthReasonMixedAgentLinks, 2, 1, 1, 1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Build("root", tt.trace, tt.graph, tt.graphErr).Signals.Subagents; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("subagents = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildBadges(t *testing.T) {
	tests := []struct {
		name     string
		trace    *model.Trace
		graph    *model.AgentGraph
		graphErr error
		want     string
	}{
		{
			name:  "no badge for exact and not applicable",
			trace: traceWith(model.ObservabilityExact, model.ObservabilityExact, withVerification(model.ObservabilityExact, 0, 0, 0, 0), withSubagents(0)),
			want:  "",
		},
		{
			name:  "estimated badge",
			trace: traceWith(model.ObservabilityEstimated, model.ObservabilityExact, withVerification(model.ObservabilityExact, 0, 0, 0, 0), withSubagents(0)),
			want:  model.HealthBadgeEstimated,
		},
		{
			name:  "limited badge for unavailable",
			trace: traceWith(model.ObservabilityUnavailable, model.ObservabilityExact, withVerification(model.ObservabilityExact, 0, 0, 0, 0), withSubagents(0)),
			want:  model.HealthBadgeLimited,
		},
		{
			name:     "limited badge for failed",
			trace:    traceWith(model.ObservabilityExact, model.ObservabilityExact, withVerification(model.ObservabilityExact, 0, 0, 0, 0), withSubagents(1)),
			graphErr: errors.New("graph failed"),
			want:     model.HealthBadgeLimited,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Build("root", tt.trace, tt.graph, tt.graphErr)
			if got.Version != model.SessionHealthVersion || got.SessionKey != "root" {
				t.Fatalf("summary identity = %#v", got)
			}
			if got.Badge != tt.want {
				t.Fatalf("badge = %q, want %q", got.Badge, tt.want)
			}
		})
	}
}

func TestBuildDoesNotMutateInputs(t *testing.T) {
	trace := traceWith(model.ObservabilityEstimated, model.ObservabilityEstimated, withVerification(model.ObservabilityEstimated, 1, 0, 1, 2), withSubagents(1))
	graph := graphWith(agent(model.AgentLinkQualityDerived, model.TraceAvailabilityMissing))
	beforeTrace, _ := json.Marshal(trace)
	beforeGraph, _ := json.Marshal(graph)

	_ = Build("root", trace, graph, nil)

	afterTrace, _ := json.Marshal(trace)
	afterGraph, _ := json.Marshal(graph)
	if !bytes.Equal(beforeTrace, afterTrace) || !bytes.Equal(beforeGraph, afterGraph) {
		t.Fatal("Build mutated its inputs")
	}
}

func traceWith(reads, errors string, options ...func(*model.Trace)) *model.Trace {
	trace := &model.Trace{
		Events: []model.Event{{Targets: []model.Target{
			{Path: "direct-a.go", Touch: "read"},
			{Path: "direct-b.go", Touch: "read"},
		}}},
		Stats: model.Stats{
			Errors:        model.ActionCounts{Read: 1, Exec: 2},
			Observability: model.Observability{Reads: reads, Errors: errors},
		},
	}
	for _, option := range options {
		option(trace)
	}
	return trace
}

func withErrors(count int) func(*model.Trace) {
	return func(trace *model.Trace) {
		trace.Stats.Errors = model.ActionCounts{Other: count}
	}
}

func withWeakRead() func(*model.Trace) {
	return func(trace *model.Trace) {
		trace.Events[0].Targets = append(trace.Events[0].Targets, model.Target{Path: "weak.go", Touch: "read", Weak: true})
	}
}

func withVerification(quality string, recognized, known, unknown, editsAfterLastVerify int) func(*model.Trace) {
	return func(trace *model.Trace) {
		trace.Stats.Actions.Verify = recognized
		trace.Stats.EditsAfterLastVerify = editsAfterLastVerify
		trace.HealthEvidence.Verification = model.VerificationEvidence{
			Quality:            quality,
			RecognizedCount:    recognized,
			KnownResultCount:   known,
			UnknownResultCount: unknown,
		}
	}
}

func withMissingVerificationEvidence(recognized int) func(*model.Trace) {
	return func(trace *model.Trace) {
		trace.Stats.Actions.Verify = recognized
	}
}

func withSubagents(count int) func(*model.Trace) {
	return func(trace *model.Trace) {
		trace.Stats.Subagents = count
	}
}

func graphWith(children ...model.AgentNode) *model.AgentGraph {
	agents := []model.AgentNode{{ID: "main", Kind: model.AgentKindMain}}
	for i := range children {
		if children[i].ParentID == "" {
			children[i].ParentID = "main"
		}
	}
	agents = append(agents, children...)
	return &model.AgentGraph{Agents: agents}
}

func agent(linkQuality, traceAvailability string) model.AgentNode {
	return model.AgentNode{Kind: model.AgentKindSubagent, LinkQuality: linkQuality, TraceAvailability: traceAvailability}
}

func readHealth(availability, quality, reason string, direct, inferred int) model.ReadHealth {
	return model.ReadHealth{HealthSignal: expectedSignal(availability, quality, reason, []string{"map", "reread-rate", "judge-exploration", "judge-wandering"}), DirectCount: direct, InferredCount: inferred}
}

func errorHealth(availability, quality, reason string, recognized int) model.ErrorHealth {
	return model.ErrorHealth{HealthSignal: expectedSignal(availability, quality, reason, []string{"error-rate", "timeline-errors", "judge-verification"}), RecognizedCount: recognized}
}

func verificationHealth(availability, quality, reason string, recognized, known, unknown, edits int) model.VerificationHealth {
	return model.VerificationHealth{HealthSignal: expectedSignal(availability, quality, reason, []string{"judge-verification"}), RecognizedCount: recognized, KnownResultCount: known, UnknownResultCount: unknown, EditsAfterLastVerify: edits}
}

func subagentHealth(availability, quality, reason string, exact, derived, missing, unavailable int) model.SubagentHealth {
	return model.SubagentHealth{HealthSignal: expectedSignal(availability, quality, reason, []string{"agent-lens"}), ExactCount: exact, DerivedCount: derived, MissingTraceCount: missing, UnavailableTraceCount: unavailable}
}

func expectedSignal(availability, quality, reason string, affects []string) model.HealthSignal {
	return model.HealthSignal{Availability: availability, Quality: quality, Reason: reason, Affects: affects}
}
