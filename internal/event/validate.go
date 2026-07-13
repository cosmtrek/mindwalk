package event

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

var (
	// ErrInvalid marks any envelope validation failure.
	ErrInvalid = errors.New("invalid event envelope")
	// ErrIdentity marks a stored EventID or NormalizedHash that does not
	// match the envelope content.
	ErrIdentity = errors.New("event identity mismatch")
)

var (
	hashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	idRe   = regexp.MustCompile(`^ev1_[0-9a-f]{32}$`)
)

// Validate checks structural validity. EventID and NormalizedHash are
// optional — Finalize sets them — but must be well-formed when present.
func Validate(e Envelope) error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schemaVersion %d, want %d", ErrInvalid, e.SchemaVersion, SchemaVersion)
	}
	if !ValidType(e.EventType) {
		return fmt.Errorf("%w: unknown eventType %q", ErrInvalid, e.EventType)
	}
	if err := checkUTC("occurredAt", e.OccurredAt); err != nil {
		return err
	}
	if err := checkUTC("observedAt", e.ObservedAt); err != nil {
		return err
	}
	if e.Sequence < 0 {
		return fmt.Errorf("%w: negative seq %d", ErrInvalid, e.Sequence)
	}
	if e.EventID != "" && !idRe.MatchString(e.EventID) {
		return fmt.Errorf("%w: malformed eventId %q", ErrInvalid, e.EventID)
	}
	if e.NormalizedHash != "" && !hashRe.MatchString(e.NormalizedHash) {
		return fmt.Errorf("%w: malformed normalizedEventHash", ErrInvalid)
	}
	for k := range e.Attrs {
		if k == "" {
			return fmt.Errorf("%w: empty attr key", ErrInvalid)
		}
	}
	for _, f := range e.RedactedFields {
		if f == "" {
			return fmt.Errorf("%w: empty redactedFields entry", ErrInvalid)
		}
	}
	return validateProvenance(e.Provenance)
}

func validateProvenance(p Provenance) error {
	if p.SourceType == "" {
		return fmt.Errorf("%w: provenance.sourceType missing", ErrInvalid)
	}
	if !ValidQuality(p.Quality) {
		return fmt.Errorf("%w: unknown quality %q", ErrInvalid, p.Quality)
	}
	for k, q := range p.FieldQuality {
		if k == "" {
			return fmt.Errorf("%w: empty fieldQuality key", ErrInvalid)
		}
		if !ValidQuality(q) {
			return fmt.Errorf("%w: unknown fieldQuality %q for %q", ErrInvalid, q, k)
		}
	}
	if p.Confidence != nil && (*p.Confidence < 0 || *p.Confidence > 1) {
		return fmt.Errorf("%w: confidence %v out of [0,1]", ErrInvalid, *p.Confidence)
	}
	if p.Quality == QualityDerived && p.Explanation == "" {
		return fmt.Errorf("%w: derived quality requires explanation", ErrInvalid)
	}
	if p.RawEventHash != nil && !hashRe.MatchString(*p.RawEventHash) {
		return fmt.Errorf("%w: malformed rawEventHash", ErrInvalid)
	}
	return nil
}

// checkUTC requires a parseable RFC3339 timestamp with a zero UTC offset.
// Finalize converts other offsets to UTC before validation.
func checkUTC(field, s string) error {
	if s == "" {
		return fmt.Errorf("%w: %s missing", ErrInvalid, field)
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalid, field, err)
	}
	if _, off := t.Zone(); off != 0 {
		return fmt.Errorf("%w: %s must be UTC", ErrInvalid, field)
	}
	return nil
}
