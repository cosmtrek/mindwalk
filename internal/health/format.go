package health

import (
	"io"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/model"
)

// WriteText writes a deterministic, human-readable SessionHealth summary.
func WriteText(w io.Writer, summary model.SessionHealth) error {
	rows := []textRow{
		{name: "File reads", signal: summary.Signals.Reads.HealthSignal},
		{name: "Errors", signal: summary.Signals.Errors.HealthSignal, noRecognized: summary.Signals.Errors.RecognizedCount == 0},
		{name: "Verification", signal: summary.Signals.Verification.HealthSignal},
		{name: "Subagents", signal: summary.Signals.Subagents.HealthSignal},
	}

	var text strings.Builder
	text.WriteString("Session health\n")
	for _, group := range []int{0, 1, 2, 3, 4} {
		for _, row := range rows {
			if textPriority(row.signal) != group {
				continue
			}
			text.WriteString(row.name)
			text.WriteString(": ")
			text.WriteString(textDescription(row))
			text.WriteByte('\n')
		}
	}
	_, err := io.WriteString(w, text.String())
	return err
}

type textRow struct {
	name         string
	signal       model.HealthSignal
	noRecognized bool
}

func textPriority(signal model.HealthSignal) int {
	switch {
	case signal.Availability == model.HealthFailed:
		return 0
	case signal.Quality == model.ObservabilityUnavailable:
		return 1
	case signal.Quality == model.ObservabilityEstimated:
		return 2
	case signal.Quality == model.ObservabilityExact:
		return 3
	default:
		return 4
	}
}

func textDescription(row textRow) string {
	switch {
	case row.signal.Availability == model.HealthFailed:
		return "Could not be computed"
	case row.signal.Quality == model.ObservabilityUnavailable:
		return "Cannot determine from this log"
	case row.signal.Quality == model.ObservabilityEstimated && row.name == "Errors" && row.noRecognized:
		return "No errors were recognized."
	case row.signal.Quality == model.ObservabilityEstimated:
		return "Partly inferred"
	case row.signal.Quality == model.ObservabilityExact:
		return "Recorded directly"
	default:
		return "Not applicable"
	}
}
