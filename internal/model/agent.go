package model

const AgentGraphVersion = 1

const (
	AgentKindMain     = "main"
	AgentKindSubagent = "subagent"

	AgentStatusMain     = "main"
	AgentStatusLaunched = "launched"
	AgentStatusFailed   = "failed"
	AgentStatusUnknown  = "unknown"

	TraceAvailabilityAvailable   = "available"
	TraceAvailabilityMissing     = "missing"
	TraceAvailabilityUnavailable = "unavailable"

	AgentLinkQualityExact       = "exact"
	AgentLinkQualityDerived     = "derived"
	AgentLinkQualityUnavailable = "unavailable"

	AgentLinkMethodRoot                     = "root"
	AgentLinkMethodCodexAgentID             = "codex-agent-id"
	AgentLinkMethodCodexParentThreadID      = "codex-parent-thread-id"
	AgentLinkMethodClaudeToolUseID          = "claude-tool-use-id"
	AgentLinkMethodClaudeSubagentsDirectory = "claude-subagents-directory"
	AgentLinkMethodUnavailable              = "unavailable"
)

type AgentGraph struct {
	Version        int         `json:"version"`
	RootSessionKey string      `json:"rootSessionKey"`
	Agents         []AgentNode `json:"agents"`
}

type AgentNode struct {
	ID                 string `json:"id"`
	ParentID           string `json:"parentId,omitempty"`
	Depth              int    `json:"depth"`
	Kind               string `json:"kind"`
	Label              string `json:"label"`
	Role               string `json:"role,omitempty"`
	InstructionPreview string `json:"instructionPreview,omitempty"`
	LaunchSeq          *int   `json:"launchSeq,omitempty"`
	LaunchCallID       string `json:"launchCallId,omitempty"`
	Status             string `json:"status"`
	TraceAvailability  string `json:"traceAvailability"`
	TraceSessionKey    string `json:"traceSessionKey,omitempty"`
	TraceEventCount    int    `json:"traceEventCount"`
	LinkQuality        string `json:"linkQuality"`
	LinkMethod         string `json:"linkMethod"`
}

type AgentSessionMeta struct {
	SourceID        string
	RootSessionID   string
	ParentSessionID string
	AgentPath       string
	Depth           int
	Label           string
	Role            string
	LaunchCallID    string
}
