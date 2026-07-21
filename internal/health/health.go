// Package health derives evidence-quality summaries from normalized session data.
package health

import "github.com/cosmtrek/mindwalk/internal/model"

var (
	readAffects         = []string{"map", "reread-rate", "judge-exploration", "judge-wandering"}
	errorAffects        = []string{"error-rate", "timeline-errors", "judge-verification"}
	verificationAffects = []string{"judge-verification"}
	subagentAffects     = []string{"agent-lens"}
)

// Build derives session health without mutating its trace or agent graph inputs.
func Build(sessionKey string, trace *model.Trace, graph *model.AgentGraph, graphErr error) model.SessionHealth {
	if trace == nil {
		trace = &model.Trace{}
	}

	summary := model.SessionHealth{
		Version:    model.SessionHealthVersion,
		SessionKey: sessionKey,
		Signals: model.HealthSignals{
			Reads:        buildReads(trace),
			Errors:       buildErrors(trace),
			Verification: buildVerification(trace),
			Subagents:    buildSubagents(trace, graph, graphErr),
		},
	}
	summary.Badge = badgeFor(summary.Signals)
	return summary
}

func buildReads(trace *model.Trace) model.ReadHealth {
	direct, inferred := 0, 0
	for _, event := range trace.Events {
		for _, target := range event.Targets {
			if target.Touch != "read" {
				continue
			}
			if target.Weak {
				inferred++
			} else {
				direct++
			}
		}
	}

	quality, reason := model.ObservabilityUnavailable, model.HealthReasonReadsUnavailable
	switch {
	case trace.Stats.Observability.Reads == model.ObservabilityExact && inferred == 0:
		quality, reason = model.ObservabilityExact, model.HealthReasonStructuredReads
	case trace.Stats.Observability.Reads == model.ObservabilityEstimated || inferred > 0:
		quality, reason = model.ObservabilityEstimated, model.HealthReasonReadsInferred
	}
	return model.ReadHealth{
		HealthSignal:  signal(model.HealthReady, quality, reason, readAffects),
		DirectCount:   direct,
		InferredCount: inferred,
	}
}

func buildErrors(trace *model.Trace) model.ErrorHealth {
	quality, reason := observability(trace.Stats.Observability.Errors, model.HealthReasonStructuredErrors, model.HealthReasonErrorsInferred, model.HealthReasonErrorsUnavailable)
	return model.ErrorHealth{
		HealthSignal:    signal(model.HealthReady, quality, reason, errorAffects),
		RecognizedCount: countActions(trace.Stats.Errors),
	}
}

func buildVerification(trace *model.Trace) model.VerificationHealth {
	evidence := trace.HealthEvidence.Verification
	recognized := evidence.RecognizedCount
	if trace.Stats.Actions.Verify > 0 && recognized == 0 {
		recognized = trace.Stats.Actions.Verify
		return model.VerificationHealth{
			HealthSignal:         signal(model.HealthReady, model.ObservabilityUnavailable, model.HealthReasonVerificationUnavailable, verificationAffects),
			RecognizedCount:      recognized,
			EditsAfterLastVerify: trace.Stats.EditsAfterLastVerify,
		}
	}

	quality, reason := model.ObservabilityExact, model.HealthReasonStructuredVerify
	if evidence.UnknownResultCount > 0 {
		quality, reason = model.ObservabilityEstimated, model.HealthReasonVerificationUnknown
	}
	return model.VerificationHealth{
		HealthSignal:         signal(model.HealthReady, quality, reason, verificationAffects),
		RecognizedCount:      recognized,
		KnownResultCount:     evidence.KnownResultCount,
		UnknownResultCount:   evidence.UnknownResultCount,
		EditsAfterLastVerify: trace.Stats.EditsAfterLastVerify,
	}
}

func buildSubagents(trace *model.Trace, graph *model.AgentGraph, graphErr error) model.SubagentHealth {
	if trace.Stats.Subagents == 0 {
		return model.SubagentHealth{HealthSignal: signal(model.HealthNotApplicable, "", model.HealthReasonNoSubagents, subagentAffects)}
	}
	if graphErr != nil {
		return model.SubagentHealth{HealthSignal: signal(model.HealthFailed, "", model.HealthReasonAgentGraphFailed, subagentAffects)}
	}
	if graph == nil {
		return model.SubagentHealth{HealthSignal: signal(model.HealthReady, model.ObservabilityUnavailable, model.HealthReasonAgentContextMissing, subagentAffects)}
	}

	summary := model.SubagentHealth{HealthSignal: signal(model.HealthReady, model.ObservabilityExact, model.HealthReasonExactAgentLinks, subagentAffects)}
	children := 0
	allExactAvailable := true
	for _, node := range graph.Agents {
		if node.Kind == model.AgentKindMain {
			continue
		}
		children++
		if node.LinkQuality != model.AgentLinkQualityExact || node.TraceAvailability != model.TraceAvailabilityAvailable {
			allExactAvailable = false
		}
		switch node.LinkQuality {
		case model.AgentLinkQualityExact:
			summary.ExactCount++
		case model.AgentLinkQualityDerived:
			summary.DerivedCount++
		}
		switch node.TraceAvailability {
		case model.TraceAvailabilityMissing:
			summary.MissingTraceCount++
		case model.TraceAvailabilityUnavailable:
			summary.UnavailableTraceCount++
		}
	}
	if children != trace.Stats.Subagents || !allExactAvailable {
		summary.Quality = model.ObservabilityEstimated
		summary.Reason = model.HealthReasonMixedAgentLinks
	}
	return summary
}

func badgeFor(signals model.HealthSignals) string {
	all := []model.HealthSignal{
		signals.Reads.HealthSignal,
		signals.Errors.HealthSignal,
		signals.Verification.HealthSignal,
		signals.Subagents.HealthSignal,
	}
	for _, signal := range all {
		if signal.Availability == model.HealthFailed || signal.Quality == model.ObservabilityUnavailable {
			return model.HealthBadgeLimited
		}
	}
	for _, signal := range all {
		if signal.Quality == model.ObservabilityEstimated {
			return model.HealthBadgeEstimated
		}
	}
	return ""
}

func observability(value, exactReason, estimatedReason, unavailableReason string) (string, string) {
	switch value {
	case model.ObservabilityExact:
		return model.ObservabilityExact, exactReason
	case model.ObservabilityEstimated:
		return model.ObservabilityEstimated, estimatedReason
	default:
		return model.ObservabilityUnavailable, unavailableReason
	}
}

func countActions(counts model.ActionCounts) int {
	return counts.Search + counts.Read + counts.Edit + counts.Exec + counts.Verify + counts.Other
}

func signal(availability, quality, reason string, affects []string) model.HealthSignal {
	return model.HealthSignal{
		Availability: availability,
		Quality:      quality,
		Reason:       reason,
		Affects:      append([]string(nil), affects...),
	}
}
