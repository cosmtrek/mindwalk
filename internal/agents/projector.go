// Package agents projects truthful, display-only agent process records from
// canonical events. Unsupported lifecycle and attribution remain UNKNOWN.
package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/cosmtrek/mindwalk/internal/event"
)

const SchemaVersion = 1

type Process struct {
	SchemaVersion       int              `json:"schemaVersion"`
	ID                  string           `json:"id"`
	SessionID           string           `json:"sessionId"`
	Kind                string           `json:"kind"`
	ParentAgentID       *string          `json:"parentAgentId,omitempty"`
	RelationshipQuality string           `json:"relationshipQuality"`
	Lifecycle           string           `json:"lifecycle"`
	LifecycleQuality    string           `json:"lifecycleQuality"`
	ControlCapability   string           `json:"controlCapability"`
	SpawnObserved       bool             `json:"spawnObserved"`
	Tools               []string         `json:"tools"`
	Files               []string         `json:"files"`
	Errors              int              `json:"errors"`
	Verifications       int              `json:"verifications"`
	Provenance          event.Provenance `json:"provenance"`
}

type Projector struct {
	sessionID string
	processes map[string]*Process
	rootID    string
}

func NewProjector(sessionID string) *Projector {
	p := &Projector{sessionID: sessionID}
	_ = p.Reset()
	return p
}

func (p *Projector) Name() string { return "agent-processes" }
func (p *Projector) Version() int { return SchemaVersion }

func (p *Projector) Reset() error {
	p.processes = map[string]*Process{}
	p.rootID = stableID("session", p.sessionID)
	return nil
}

func (p *Projector) Apply(envelope event.Envelope) error {
	if envelope.SessionID == nil || *envelope.SessionID != p.sessionID {
		return nil
	}
	root := p.ensureRoot(envelope)
	if tool := envelope.Attrs["tool"]; tool != "" {
		root.Tools = appendUnique(root.Tools, tool)
	}
	if path := envelope.Attrs["path"]; path != "" {
		root.Files = appendUnique(root.Files, path)
	}
	if envelope.EventType == event.TypeToolFailed || envelope.Attrs["error"] == "true" {
		root.Errors++
	}
	if envelope.EventType == event.TypeVerifyCompleted {
		root.Verifications++
	}
	if envelope.EventType == event.TypeAgentSpawned {
		id := stableID("spawn", envelope.EventID)
		parent := p.rootID
		kind := envelope.Attrs["agentKind"]
		if kind == "" {
			kind = "UNKNOWN"
		}
		p.processes[id] = &Process{
			SchemaVersion: SchemaVersion, ID: id, SessionID: p.sessionID, Kind: kind,
			ParentAgentID: &parent, RelationshipQuality: event.QualityDerived,
			Lifecycle: "UNKNOWN", LifecycleQuality: event.QualityUnavailable,
			ControlCapability: "DISPLAY_ONLY", SpawnObserved: true,
			Tools: []string{}, Files: []string{}, Provenance: derived(envelope),
		}
	}
	return nil
}

func (p *Projector) ensureRoot(envelope event.Envelope) *Process {
	if process := p.processes[p.rootID]; process != nil {
		return process
	}
	process := &Process{
		SchemaVersion: SchemaVersion, ID: p.rootID, SessionID: p.sessionID,
		Kind: "session", RelationshipQuality: event.QualityUnavailable,
		Lifecycle: "UNKNOWN", LifecycleQuality: event.QualityUnavailable,
		ControlCapability: "DISPLAY_ONLY", SpawnObserved: envelope.EventType == event.TypeSessionStarted,
		Tools: []string{}, Files: []string{}, Provenance: derived(envelope),
	}
	p.processes[p.rootID] = process
	return process
}

func (p *Projector) Snapshot() []Process {
	out := make([]Process, 0, len(p.processes))
	for _, process := range p.processes {
		copy := *process
		copy.Tools = append([]string(nil), process.Tools...)
		copy.Files = append([]string(nil), process.Files...)
		sort.Strings(copy.Tools)
		sort.Strings(copy.Files)
		out = append(out, copy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func stableID(kind, value string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + value))
	return "agt_" + hex.EncodeToString(sum[:12])
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func derived(envelope event.Envelope) event.Provenance {
	sourceID := envelope.EventID
	confidence := float64(1)
	return event.Provenance{
		SourceType: "canonical-event", SourceName: envelope.Provenance.SourceName,
		SourceEventID: &sourceID, Quality: event.QualityDerived, Confidence: &confidence,
		Explanation: "projected from canonical observable events; unsupported lifecycle remains UNKNOWN",
	}
}
