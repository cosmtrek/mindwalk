package ingest

import (
	"strconv"

	"github.com/cosmtrek/mindwalk/internal/event"
)

type ObservableProjection struct {
	SchemaVersion int                       `json:"schemaVersion"`
	SessionID     string                    `json:"sessionId"`
	RepositoryID  string                    `json:"repositoryId,omitempty"`
	EventCount    int                       `json:"eventCount"`
	Files         map[string]FileProjection `json:"files"`
	Verifications int                       `json:"verifications"`
	Errors        int                       `json:"errors"`
	Agents        int                       `json:"agents"`
}

type FileProjection struct {
	Touches   int    `json:"touches"`
	LastTouch string `json:"lastTouch"`
}

type ObservableProjector struct {
	sessionID string
	state     ObservableProjection
}

func NewObservableProjector(sessionID string) *ObservableProjector {
	p := &ObservableProjector{sessionID: sessionID}
	_ = p.Reset()
	return p
}

func (p *ObservableProjector) Name() string { return "observable-session" }
func (p *ObservableProjector) Version() int { return 1 }

func (p *ObservableProjector) Reset() error {
	p.state = ObservableProjection{SchemaVersion: 1, SessionID: p.sessionID, Files: map[string]FileProjection{}}
	return nil
}

func (p *ObservableProjector) Apply(envelope event.Envelope) error {
	if envelope.SessionID == nil || *envelope.SessionID != p.sessionID {
		return nil
	}
	p.state.EventCount++
	if envelope.RepoID != nil {
		p.state.RepositoryID = *envelope.RepoID
	}
	if path := envelope.Attrs["path"]; path != "" {
		file := p.state.Files[path]
		file.Touches++
		file.LastTouch = envelope.Attrs["touch"]
		p.state.Files[path] = file
	}
	if envelope.EventType == event.TypeVerifyCompleted {
		p.state.Verifications++
	}
	if envelope.EventType == event.TypeToolFailed {
		p.state.Errors++
	} else if failed, _ := strconv.ParseBool(envelope.Attrs["error"]); failed {
		p.state.Errors++
	}
	if envelope.EventType == event.TypeAgentSpawned {
		p.state.Agents++
	}
	return nil
}

func (p *ObservableProjector) Snapshot() ObservableProjection { return p.state }
