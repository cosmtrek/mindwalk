package health

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestWriteTextOrdersSignalsAndUsesEvidenceLanguage(t *testing.T) {
	summary := model.SessionHealth{SessionKey: "secret task event summary src/private.go tool output", Signals: model.HealthSignals{
		Reads: model.ReadHealth{HealthSignal: model.HealthSignal{
			Availability: model.HealthReady,
			Quality:      model.ObservabilityExact,
			Reason:       model.HealthReasonStructuredReads,
		}},
		Errors: model.ErrorHealth{HealthSignal: model.HealthSignal{
			Availability: model.HealthReady,
			Quality:      model.ObservabilityEstimated,
			Reason:       model.HealthReasonErrorsInferred,
		}},
		Verification: model.VerificationHealth{HealthSignal: model.HealthSignal{
			Availability: model.HealthReady,
			Quality:      model.ObservabilityUnavailable,
			Reason:       model.HealthReasonVerificationUnavailable,
		}},
		Subagents: model.SubagentHealth{HealthSignal: model.HealthSignal{
			Availability: model.HealthFailed,
			Reason:       model.HealthReasonAgentGraphFailed,
		}},
	}}

	var output bytes.Buffer
	if err := WriteText(&output, summary); err != nil {
		t.Fatal(err)
	}

	want := "Session health\n" +
		"Subagents: Could not be computed\n" +
		"Verification: Cannot determine from this log\n" +
		"Errors: No errors were recognized.\n" +
		"File reads: Recorded directly\n"
	if got := output.String(); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	for _, private := range []string{"agent-graph-load-failed", "secret task", "event summary", "src/private.go", "tool output", "\x1b"} {
		if strings.Contains(output.String(), private) {
			t.Fatalf("text leaks %q: %q", private, output.String())
		}
	}
}

func TestWriteTextRendersNotApplicableLast(t *testing.T) {
	summary := model.SessionHealth{Signals: model.HealthSignals{
		Reads: model.ReadHealth{HealthSignal: model.HealthSignal{
			Availability: model.HealthNotApplicable,
			Reason:       model.HealthReasonNoSubagents,
		}},
		Errors: model.ErrorHealth{HealthSignal: model.HealthSignal{
			Availability: model.HealthReady,
			Quality:      model.ObservabilityExact,
			Reason:       model.HealthReasonStructuredErrors,
		}},
		Verification: model.VerificationHealth{HealthSignal: model.HealthSignal{
			Availability: model.HealthReady,
			Quality:      model.ObservabilityExact,
			Reason:       model.HealthReasonStructuredVerify,
		}},
		Subagents: model.SubagentHealth{HealthSignal: model.HealthSignal{
			Availability: model.HealthNotApplicable,
			Reason:       model.HealthReasonNoSubagents,
		}},
	}}

	var output bytes.Buffer
	if err := WriteText(&output, summary); err != nil {
		t.Fatal(err)
	}
	want := "Session health\n" +
		"Errors: Recorded directly\n" +
		"Verification: Recorded directly\n" +
		"File reads: Not applicable\n" +
		"Subagents: Not applicable\n"
	if got := output.String(); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestWriteTextReturnsWriterError(t *testing.T) {
	errExpected := errors.New("writer failed")
	if err := WriteText(errorWriter{err: errExpected}, model.SessionHealth{}); !errors.Is(err, errExpected) {
		t.Fatalf("WriteText error = %v, want %v", err, errExpected)
	}
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}
