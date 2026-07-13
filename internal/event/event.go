// Package event defines the canonical EventEnvelope: the versioned, hashed
// record every Observatory data source normalizes into. Envelopes carry
// metadata only — raw source content is never stored, only its SHA-256.
package event

// SchemaVersion is the only envelope schema version this package accepts.
const SchemaVersion = 1

// Quality grades how a value was obtained. The first three values match the
// vocabulary already used by model.Observability; "derived" marks values
// computed from other events, "redacted" values withheld by policy.
const (
	QualityExact       = "exact"
	QualityEstimated   = "estimated"
	QualityDerived     = "derived"
	QualityUnavailable = "unavailable"
	QualityRedacted    = "redacted"
)

// Canonical event types. Adapters must map source records onto these; a type
// outside this list fails validation.
const (
	TypeRepoDiscovered = "repo.discovered"
	TypeRepoChanged    = "repo.changed"
	TypeRepoRemoved    = "repo.removed"

	TypeSessionStarted   = "session.started"
	TypeSessionUpdated   = "session.updated"
	TypeSessionCompleted = "session.completed"
	TypeSessionFailed    = "session.failed"

	TypeAgentSpawned   = "agent.spawned"
	TypeAgentStarted   = "agent.started"
	TypeAgentPaused    = "agent.paused"
	TypeAgentResumed   = "agent.resumed"
	TypeAgentCompleted = "agent.completed"
	TypeAgentFailed    = "agent.failed"
	TypeAgentCancelled = "agent.cancelled"

	TypeModelRequest  = "model.request"
	TypeModelResponse = "model.response"
	TypeModelError    = "model.error"

	TypeToolRequested = "tool.requested"
	TypeToolStarted   = "tool.started"
	TypeToolCompleted = "tool.completed"
	TypeToolFailed    = "tool.failed"
	TypeToolBlocked   = "tool.blocked"

	TypeFileSearched = "file.searched"
	TypeFileRead     = "file.read"
	TypeFileEdited   = "file.edited"
	TypeFileCreated  = "file.created"
	TypeFileDeleted  = "file.deleted"

	TypeCommandStarted   = "command.started"
	TypeCommandCompleted = "command.completed"

	TypeVerifyStarted   = "verify.started"
	TypeVerifyCompleted = "verify.completed"

	TypeGitStatus         = "git.status"
	TypeGitDiffObserved   = "git.diff_observed"
	TypeGitCommitObserved = "git.commit_observed"
	TypeGitPRObserved     = "git.pr_observed"

	TypeMemorySearch        = "memory.search"
	TypeMemoryRead          = "memory.read"
	TypeMemoryProposedWrite = "memory.proposed_write"
	TypeMemoryWrite         = "memory.write"
	TypeMemoryCorrected     = "memory.corrected"
	TypeMemoryDeleted       = "memory.deleted"

	TypeApprovalRequested = "approval.requested"
	TypeApprovalApproved  = "approval.approved"
	TypeApprovalDenied    = "approval.denied"
	TypeApprovalExpired   = "approval.expired"

	TypeSecurityRedaction   = "security.redaction"
	TypeSecurityPolicyBlock = "security.policy_block"
	TypeSecurityAlert       = "security.alert"

	TypeContextCompacted = "context.compacted"
	TypeUserMessage      = "user.message"
	TypeSystemHeartbeat  = "system.heartbeat"
)

// AllTypes lists every canonical event type.
var AllTypes = []string{
	TypeRepoDiscovered, TypeRepoChanged, TypeRepoRemoved,
	TypeSessionStarted, TypeSessionUpdated, TypeSessionCompleted, TypeSessionFailed,
	TypeAgentSpawned, TypeAgentStarted, TypeAgentPaused, TypeAgentResumed,
	TypeAgentCompleted, TypeAgentFailed, TypeAgentCancelled,
	TypeModelRequest, TypeModelResponse, TypeModelError,
	TypeToolRequested, TypeToolStarted, TypeToolCompleted, TypeToolFailed, TypeToolBlocked,
	TypeFileSearched, TypeFileRead, TypeFileEdited, TypeFileCreated, TypeFileDeleted,
	TypeCommandStarted, TypeCommandCompleted,
	TypeVerifyStarted, TypeVerifyCompleted,
	TypeGitStatus, TypeGitDiffObserved, TypeGitCommitObserved, TypeGitPRObserved,
	TypeMemorySearch, TypeMemoryRead, TypeMemoryProposedWrite, TypeMemoryWrite,
	TypeMemoryCorrected, TypeMemoryDeleted,
	TypeApprovalRequested, TypeApprovalApproved, TypeApprovalDenied, TypeApprovalExpired,
	TypeSecurityRedaction, TypeSecurityPolicyBlock, TypeSecurityAlert,
	TypeContextCompacted, TypeUserMessage, TypeSystemHeartbeat,
}

var typeSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(AllTypes))
	for _, t := range AllTypes {
		m[t] = struct{}{}
	}
	return m
}()

// ValidType reports whether t is a canonical event type.
func ValidType(t string) bool {
	_, ok := typeSet[t]
	return ok
}

// ValidQuality reports whether q is a known quality grade.
func ValidQuality(q string) bool {
	switch q {
	case QualityExact, QualityEstimated, QualityDerived, QualityUnavailable, QualityRedacted:
		return true
	}
	return false
}

// Provenance records where an envelope's facts came from and how much to
// trust them.
type Provenance struct {
	SourceType    string  `json:"sourceType"`
	SourceName    string  `json:"sourceName,omitempty"`
	SourceEventID *string `json:"sourceEventId,omitempty"`
	// RawEventHash is the SHA-256 (lowercase hex) of the raw source record.
	// The raw content itself is never stored in an envelope.
	RawEventHash *string `json:"rawEventHash,omitempty"`
	Quality      string  `json:"quality"`
	// FieldQuality overrides Quality per field when one record mixes grades.
	FieldQuality map[string]string `json:"fieldQuality,omitempty"`
	Confidence   *float64          `json:"confidence,omitempty"`
	// Explanation says how a value was derived; required when Quality is
	// "derived".
	Explanation string `json:"explanation,omitempty"`
}

// Envelope is the canonical event record. Optional identity fields are
// pointers so a value that was never recorded stays distinguishable from a
// recorded empty value.
type Envelope struct {
	SchemaVersion int `json:"schemaVersion"`
	// EventID is derived from NormalizedHash by Finalize; it is never an
	// input.
	EventID       string  `json:"eventId,omitempty"`
	EventType     string  `json:"eventType"`
	OccurredAt    string  `json:"occurredAt"` // RFC3339, UTC
	ObservedAt    string  `json:"observedAt"` // RFC3339, UTC
	Sequence      int64   `json:"seq"`
	RepoID        *string `json:"repoId,omitempty"`
	SessionID     *string `json:"sessionId,omitempty"`
	AgentID       *string `json:"agentId,omitempty"`
	ParentAgentID *string `json:"parentAgentId,omitempty"`
	ParentEventID *string `json:"parentEventId,omitempty"`
	// Attrs holds normalized metadata only (tool name, path token, exit
	// status, counts) — never message or file content.
	Attrs          map[string]string `json:"attrs,omitempty"`
	RedactedFields []string          `json:"redactedFields,omitempty"`
	Provenance     Provenance        `json:"provenance"`
	// NormalizedHash is the SHA-256 over CanonicalJSON of the envelope with
	// EventID and NormalizedHash cleared; set by Finalize.
	NormalizedHash string `json:"normalizedEventHash,omitempty"`
}
