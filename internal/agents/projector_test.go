package agents

import (
	"reflect"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/event"
)

func TestProjectorBuildsDisplayOnlyProcessesWithoutInventingLifecycle(t *testing.T) {
	session := "claude-key"
	repo := "repo_fixture"
	events := []event.Envelope{
		finalize(t, event.Envelope{SchemaVersion: 1, EventType: event.TypeSessionStarted, OccurredAt: "2026-07-13T00:00:00Z", ObservedAt: "2026-07-13T00:00:00Z", RepoID: &repo, SessionID: &session, Attrs: map[string]string{"harness": "claude-code"}, Provenance: provenance()}),
		finalize(t, event.Envelope{SchemaVersion: 1, EventType: event.TypeFileEdited, OccurredAt: "2026-07-13T00:00:01Z", ObservedAt: "2026-07-13T00:00:01Z", RepoID: &repo, SessionID: &session, Attrs: map[string]string{"tool": "Edit", "path": "README.md"}, Provenance: provenance()}),
		finalize(t, event.Envelope{SchemaVersion: 1, EventType: event.TypeVerifyCompleted, OccurredAt: "2026-07-13T00:00:02Z", ObservedAt: "2026-07-13T00:00:02Z", RepoID: &repo, SessionID: &session, Attrs: map[string]string{"tool": "Bash", "error": "true"}, Provenance: provenance()}),
		finalize(t, event.Envelope{SchemaVersion: 1, EventType: event.TypeAgentSpawned, OccurredAt: "2026-07-13T00:00:03Z", ObservedAt: "2026-07-13T00:00:03Z", RepoID: &repo, SessionID: &session, Attrs: map[string]string{"agentKind": "Task"}, Provenance: provenance()}),
	}
	p := NewProjector(session)
	for _, envelope := range events {
		if err := p.Apply(envelope); err != nil {
			t.Fatal(err)
		}
	}
	got := p.Snapshot()
	if len(got) != 2 {
		t.Fatalf("processes = %#v", got)
	}
	var root, child Process
	for _, process := range got {
		if process.Kind == "session" {
			root = process
		} else {
			child = process
		}
	}
	if root.Lifecycle != "UNKNOWN" || root.ControlCapability != "DISPLAY_ONLY" || root.Verifications != 1 || root.Errors != 1 || !reflect.DeepEqual(root.Files, []string{"README.md"}) {
		t.Fatalf("root = %#v", root)
	}
	if child.Kind != "Task" || child.ParentAgentID == nil || *child.ParentAgentID != root.ID || child.Lifecycle != "UNKNOWN" || child.RelationshipQuality != event.QualityDerived {
		t.Fatalf("child = %#v root=%#v", child, root)
	}
	first := p.Snapshot()
	if err := p.Reset(); err != nil {
		t.Fatal(err)
	}
	for _, envelope := range events {
		if err := p.Apply(envelope); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(first, p.Snapshot()) {
		t.Fatal("agent projection is not deterministic")
	}
}

func finalize(t *testing.T, envelope event.Envelope) event.Envelope {
	t.Helper()
	finalized, err := event.Finalize(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return finalized
}

func provenance() event.Provenance {
	confidence := float64(1)
	return event.Provenance{SourceType: "fixture", Quality: event.QualityDerived, Confidence: &confidence, Explanation: "synthetic fixture"}
}
