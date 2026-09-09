//nolint:testpackage,goconst // Match the rest of the model test files (agent_schema_test, schema_test, etc).
package model

import "testing"

// FuzzComputeStats exercises ComputeStats with arbitrary trace
// shapes and observability signals. The invariant: the function must
// never panic and must always populate the observability grade —
// it can be Exact, Estimated, or Unavailable, but never empty or
// unrecognised.
func FuzzComputeStats(f *testing.F) {
	f.Add(uint8(0), uint8(1), "exact", "exact")
	f.Add(uint8(1), uint8(0), "", "estimated")
	f.Add(uint8(2), uint8(2), "garbage", "")
	f.Add(uint8(3), uint8(0), "exact", "")
	f.Add(uint8(0), uint8(5), "", "")
	f.Fuzz(func(t *testing.T, actionFlag, errorFlag uint8, errorSig, readsSig string) {
		actions := []string{"search", "read", "edit", "exec", "verify"}
		action := actions[int(actionFlag)%len(actions)]

		trace := &Trace{
			Events: []Event{
				{
					Seq:          0,
					Action:       action,
					IsError:      errorFlag%2 == 1,
					OutcomeKnown: errorFlag/2%2 == 1,
					Targets: []Target{
						{Path: "a.go", Touch: "read", Weak: errorFlag%3 == 0},
					},
					ResultBytes: int(errorFlag),
				},
			},
		}

		stats := ComputeStats(trace, int(actionFlag), ObservabilitySignals{
			Errors: errorSig,
			Reads:  readsSig,
		})

		// Grade must always be one of the three known values.
		switch stats.Observability.Errors {
		case ObservabilityExact, ObservabilityEstimated, ObservabilityUnavailable:
		default:
			t.Fatalf("unexpected errors grade %q", stats.Observability.Errors)
		}

		switch stats.Observability.Reads {
		case ObservabilityExact, ObservabilityEstimated, ObservabilityUnavailable:
		default:
			t.Fatalf("unexpected reads grade %q", stats.Observability.Reads)
		}
	})
}

// TestComputeStatsNilEvents pins the panic-free behaviour for a nil
// Events slice. Item 36 in the post-merge plan calls this out
// explicitly — without it, a downstream caller that builds a Trace{}
// by hand (e.g. during replay) can crash the server.
func TestComputeStatsNilEvents(t *testing.T) {
	t.Parallel()

	stats := ComputeStats(&Trace{}, 0, ObservabilitySignals{Errors: ObservabilityExact})

	if stats.Actions != (ActionCounts{}) {
		t.Fatalf("actions = %#v, want zero", stats.Actions)
	}

	if stats.ErrorRate != 0 {
		t.Fatalf("errorRate = %v, want 0", stats.ErrorRate)
	}

	if stats.EventsBeforeFirstEdit != 0 {
		t.Fatalf("eventsBeforeFirstEdit = %d, want 0 (no events)", stats.EventsBeforeFirstEdit)
	}

	if stats.Observability.Reads != ObservabilityUnavailable {
		t.Fatalf("reads = %q, want unavailable (no reads)", stats.Observability.Reads)
	}
}
