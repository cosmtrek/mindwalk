package model

const SessionHealthVersion = 1

const (
	HealthReady         = "ready"
	HealthNotApplicable = "not-applicable"
	HealthFailed        = "failed"

	HealthReasonStructuredReads         = "structured-read-targets"
	HealthReasonReadsInferred           = "some-reads-inferred-from-shell"
	HealthReasonReadsUnavailable        = "read-signal-unavailable"
	HealthReasonStructuredErrors        = "structured-error-status"
	HealthReasonErrorsInferred          = "errors-inferred-from-output"
	HealthReasonErrorsUnavailable       = "error-signal-unavailable"
	HealthReasonStructuredVerify        = "structured-verification-results"
	HealthReasonVerificationInferred    = "verification-command-recognition-inferred"
	HealthReasonVerificationUnknown     = "some-verification-results-unknown"
	HealthReasonVerificationUnavailable = "verification-signal-unavailable"
	HealthReasonNoSubagents             = "no-subagents"
	HealthReasonExactAgentLinks         = "exact-agent-links"
	HealthReasonMixedAgentLinks         = "mixed-agent-link-quality"
	HealthReasonAgentContextMissing     = "agent-graph-context-unavailable"
	HealthReasonAgentGraphFailed        = "agent-graph-load-failed"
)

type SessionHealth struct {
	Version    int           `json:"version"`
	SessionKey string        `json:"sessionKey"`
	Badge      string        `json:"badge,omitempty"`
	Signals    HealthSignals `json:"signals"`
}

const (
	HealthBadgeEstimated = "estimated"
	HealthBadgeLimited   = "limited"
)

type HealthSignals struct {
	Reads        ReadHealth         `json:"reads"`
	Errors       ErrorHealth        `json:"errors"`
	Verification VerificationHealth `json:"verification"`
	Subagents    SubagentHealth     `json:"subagents"`
}

type HealthSignal struct {
	Availability string   `json:"availability"`
	Quality      string   `json:"quality,omitempty"`
	Reason       string   `json:"reason"`
	Affects      []string `json:"affects"`
}

type ReadHealth struct {
	HealthSignal
	DirectCount   int `json:"directCount"`
	InferredCount int `json:"inferredCount"`
}

type ErrorHealth struct {
	HealthSignal
	RecognizedCount int `json:"recognizedCount"`
}

type VerificationHealth struct {
	HealthSignal
	RecognizedCount      int `json:"recognizedCount"`
	KnownResultCount     int `json:"knownResultCount"`
	UnknownResultCount   int `json:"unknownResultCount"`
	EditsAfterLastVerify int `json:"editsAfterLastVerify"`
}

type SubagentHealth struct {
	HealthSignal
	ExactCount            int `json:"exactCount"`
	DerivedCount          int `json:"derivedCount"`
	MissingTraceCount     int `json:"missingTraceCount"`
	UnavailableTraceCount int `json:"unavailableTraceCount"`
}

type TraceHealthEvidence struct {
	Verification VerificationEvidence `json:"-"`
}

type VerificationEvidence struct {
	Quality            string
	RecognizedCount    int
	KnownResultCount   int
	UnknownResultCount int
}
