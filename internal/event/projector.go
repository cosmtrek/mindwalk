package event

import "fmt"

// Projector consumes envelopes in ledger order to build a read model.
// Implementations must be deterministic: Reset followed by the same event
// sequence must rebuild the identical state, so projections can always be
// regenerated from the ledger.
type Projector interface {
	// Name identifies the projector in diagnostics and stored state.
	Name() string
	// Version invalidates stored projections when projection logic changes.
	Version() int
	// Apply folds one verified envelope into the read model.
	Apply(Envelope) error
	// Reset clears the read model before a replay.
	Reset() error
}

// Replay resets every projector, then feeds it the full ledger at path in
// append order. Quarantined or torn content is skipped by ReadAll, so replay
// of a damaged ledger still projects every verified event.
func Replay(path string, projectors ...Projector) error {
	events, err := ReadAll(path)
	if err != nil {
		return err
	}
	for _, p := range projectors {
		if err := p.Reset(); err != nil {
			return fmt.Errorf("projector %s: reset: %w", p.Name(), err)
		}
	}
	for _, e := range events {
		for _, p := range projectors {
			if err := p.Apply(e); err != nil {
				return fmt.Errorf("projector %s: event %s: %w", p.Name(), e.EventID, err)
			}
		}
	}
	return nil
}
