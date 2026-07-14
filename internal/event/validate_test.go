package event

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateRejections(t *testing.T) {
	conf := 1.5
	badRaw := "not-a-hash"
	cases := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{"wrong schema version", func(e *Envelope) { e.SchemaVersion = 2 }},
		{"unknown event type", func(e *Envelope) { e.EventType = "thought.private" }},
		{"empty occurredAt", func(e *Envelope) { e.OccurredAt = "" }},
		{"garbage occurredAt", func(e *Envelope) { e.OccurredAt = "yesterday" }},
		{"non-UTC observedAt", func(e *Envelope) { e.ObservedAt = "2026-07-13T10:00:00+02:00" }},
		{"negative sequence", func(e *Envelope) { e.Sequence = -1 }},
		{"malformed eventId", func(e *Envelope) { e.EventID = "ev2_zzzz" }},
		{"malformed normalized hash", func(e *Envelope) { e.NormalizedHash = "abc" }},
		{"empty attr key", func(e *Envelope) { e.Attrs = map[string]string{"": "x"} }},
		{"empty redacted field", func(e *Envelope) { e.RedactedFields = []string{""} }},
		{"missing sourceType", func(e *Envelope) { e.Provenance.SourceType = "" }},
		{"unknown quality", func(e *Envelope) { e.Provenance.Quality = "perfect" }},
		{"unknown field quality", func(e *Envelope) {
			e.Provenance.FieldQuality = map[string]string{"occurredAt": "guessed"}
		}},
		{"confidence out of range", func(e *Envelope) { e.Provenance.Confidence = &conf }},
		{"derived without explanation", func(e *Envelope) { e.Provenance.Quality = QualityDerived }},
		{"malformed rawEventHash", func(e *Envelope) { e.Provenance.RawEventHash = &badRaw }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := synthetic()
			tc.mutate(&e)
			err := Validate(e)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v, want ErrInvalid", err)
			}
		})
	}
}

func TestValidateAcceptsUnfinalizedAndFinalized(t *testing.T) {
	e := synthetic()
	if err := Validate(e); err != nil {
		t.Fatalf("unfinalized synthetic envelope rejected: %v", err)
	}
	fin, err := Finalize(e)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if err := Validate(fin); err != nil {
		t.Fatalf("finalized envelope rejected: %v", err)
	}
}

func TestValidateErrorsAreClear(t *testing.T) {
	e := synthetic()
	e.EventType = "thought.private"
	err := Validate(e)
	if err == nil || !strings.Contains(err.Error(), "thought.private") {
		t.Fatalf("error does not name the offending value: %v", err)
	}
}
