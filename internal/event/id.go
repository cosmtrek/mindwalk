package event

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// idPrefix marks event IDs derived from a v1 normalized hash.
const idPrefix = "ev1_"

// CanonicalJSON returns the deterministic serialization identity is computed
// over: compact encoding/json output — struct fields in declaration order,
// map keys sorted — with EventID and NormalizedHash cleared. e is passed by
// value, so the caller's envelope is untouched.
func CanonicalJSON(e Envelope) ([]byte, error) {
	e.EventID = ""
	e.NormalizedHash = ""
	return json.Marshal(e)
}

// Finalize normalizes the envelope's timestamps to canonical UTC form,
// validates it, and computes NormalizedHash and EventID. Identical normalized
// inputs always yield identical identity; finalizing a finalized envelope is
// a no-op.
func Finalize(e Envelope) (Envelope, error) {
	e.EventID = ""
	e.NormalizedHash = ""
	var err error
	if e.OccurredAt, err = canonicalTime(e.OccurredAt); err != nil {
		return Envelope{}, fmt.Errorf("%w: occurredAt: %v", ErrInvalid, err)
	}
	if e.ObservedAt, err = canonicalTime(e.ObservedAt); err != nil {
		return Envelope{}, fmt.Errorf("%w: observedAt: %v", ErrInvalid, err)
	}
	if err := Validate(e); err != nil {
		return Envelope{}, err
	}
	b, err := CanonicalJSON(e)
	if err != nil {
		return Envelope{}, err
	}
	sum := sha256.Sum256(b)
	e.NormalizedHash = hex.EncodeToString(sum[:])
	e.EventID = idPrefix + e.NormalizedHash[:32]
	return e, nil
}

// Verify recomputes a finalized envelope's identity and reports whether the
// stored EventID and NormalizedHash still match its content.
func Verify(e Envelope) error {
	re, err := Finalize(e)
	if err != nil {
		return err
	}
	if re.EventID != e.EventID || re.NormalizedHash != e.NormalizedHash {
		return fmt.Errorf("%w: stored identity does not match content", ErrIdentity)
	}
	return nil
}

// canonicalTime parses an RFC3339 timestamp and reformats it as RFC3339Nano
// in UTC, so every spelling of the same instant shares one canonical form.
func canonicalTime(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("missing")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return "", err
	}
	return t.UTC().Format(time.RFC3339Nano), nil
}
