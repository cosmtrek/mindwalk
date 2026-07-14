package event

import (
	"errors"
	"strings"
	"testing"
)

// synthetic returns a fully synthetic, redaction-safe envelope fixture.
func synthetic() Envelope {
	sid := "sess-fixture-001"
	raw := strings.Repeat("ab", 32)
	return Envelope{
		SchemaVersion: SchemaVersion,
		EventType:     TypeFileRead,
		OccurredAt:    "2026-07-13T10:00:00Z",
		ObservedAt:    "2026-07-13T10:00:01Z",
		Sequence:      7,
		SessionID:     &sid,
		Attrs: map[string]string{
			"pathToken": "tok_0011",
			"tool":      "read",
		},
		Provenance: Provenance{
			SourceType:   "synthetic_test",
			SourceName:   "fixture",
			RawEventHash: &raw,
			Quality:      QualityExact,
		},
	}
}

func TestFinalizeDeterministic(t *testing.T) {
	a, err := Finalize(synthetic())
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	b, err := Finalize(synthetic())
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if a.EventID != b.EventID || a.NormalizedHash != b.NormalizedHash {
		t.Fatalf("identical inputs produced different identity: %q vs %q", a.EventID, b.EventID)
	}
	if !idRe.MatchString(a.EventID) {
		t.Fatalf("malformed eventId %q", a.EventID)
	}
	if !hashRe.MatchString(a.NormalizedHash) {
		t.Fatalf("malformed normalizedEventHash %q", a.NormalizedHash)
	}
}

func TestIdentityChangesWithContent(t *testing.T) {
	a, _ := Finalize(synthetic())
	e := synthetic()
	e.Attrs["tool"] = "grep"
	b, err := Finalize(e)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if a.EventID == b.EventID {
		t.Fatal("different content produced the same eventId")
	}
}

func TestMapInsertionOrderIrrelevant(t *testing.T) {
	e1 := synthetic()
	e1.Attrs = map[string]string{}
	for _, k := range []string{"a", "b", "c", "d"} {
		e1.Attrs[k] = "v-" + k
	}
	e2 := synthetic()
	e2.Attrs = map[string]string{}
	for _, k := range []string{"d", "c", "b", "a"} {
		e2.Attrs[k] = "v-" + k
	}
	a, err := Finalize(e1)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	b, err := Finalize(e2)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if a.EventID != b.EventID {
		t.Fatal("attr insertion order changed identity")
	}
}

func TestTimezoneSpellingIrrelevant(t *testing.T) {
	e1 := synthetic()
	e1.OccurredAt = "2026-07-13T12:00:00+02:00"
	e2 := synthetic()
	e2.OccurredAt = "2026-07-13T10:00:00Z"
	a, err := Finalize(e1)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	b, err := Finalize(e2)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if a.EventID != b.EventID {
		t.Fatal("timezone spelling of the same instant changed identity")
	}
	if a.OccurredAt != "2026-07-13T10:00:00Z" {
		t.Fatalf("occurredAt not canonicalized to UTC: %q", a.OccurredAt)
	}
}

func TestFinalizeIdempotent(t *testing.T) {
	once, err := Finalize(synthetic())
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	twice, err := Finalize(once)
	if err != nil {
		t.Fatalf("Finalize(finalized): %v", err)
	}
	if once.EventID != twice.EventID || once.NormalizedHash != twice.NormalizedHash {
		t.Fatal("finalizing a finalized envelope changed its identity")
	}
}

func TestVerify(t *testing.T) {
	e, err := Finalize(synthetic())
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if err := Verify(e); err != nil {
		t.Fatalf("Verify of untampered envelope: %v", err)
	}
	tampered := e
	tampered.Attrs = map[string]string{"pathToken": "tok_0011", "tool": "write"}
	if err := Verify(tampered); !errors.Is(err, ErrIdentity) {
		t.Fatalf("Verify of tampered content: got %v, want ErrIdentity", err)
	}
	badID := e
	badID.EventID = "ev1_" + strings.Repeat("0", 32)
	if err := Verify(badID); !errors.Is(err, ErrIdentity) {
		t.Fatalf("Verify of swapped eventId: got %v, want ErrIdentity", err)
	}
}

func TestCanonicalJSONExcludesIdentity(t *testing.T) {
	raw := synthetic()
	fin, err := Finalize(raw)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	a, err := CanonicalJSON(fin)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	b, err := CanonicalJSON(raw)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if string(a) != string(b) {
		t.Fatal("identity fields leaked into the canonical serialization")
	}
	if strings.Contains(string(a), fin.EventID) {
		t.Fatal("canonical bytes contain the eventId")
	}
}

// TestGoldenIdentity pins the canonical serialization contract: if this
// breaks, the on-disk identity of every stored event breaks with it.
func TestGoldenIdentity(t *testing.T) {
	e, err := Finalize(synthetic())
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	const (
		wantID   = "ev1_9f422755dceaf4444dd1286786c63293"
		wantHash = "9f422755dceaf4444dd1286786c632931f9a07cd5b05b4a9af30735404ae3720"
	)
	if e.EventID != wantID {
		t.Errorf("eventId drifted: got %q, want %q", e.EventID, wantID)
	}
	if e.NormalizedHash != wantHash {
		t.Errorf("normalizedEventHash drifted: got %q, want %q", e.NormalizedHash, wantHash)
	}
}
