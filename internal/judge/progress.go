package judge

// Progress is a step-level update emitted during an evaluation run. The
// server streams these to the browser via SSE so the user sees which phase
// the judge is in instead of an opaque "Judging…" spinner.
type Progress struct {
	// Phase is the coarse pipeline stage: "start", "rubric", "scoring",
	// "done", or "error".
	Phase string `json:"phase"`
	// Step qualifies the phase: "skip", "reuse", "generate", "complete",
	// "retry", "fail", or "" when the phase needs no sub-step.
	Step string `json:"step,omitempty"`
	// Message is a human-readable description safe to show as-is.
	Message string `json:"message"`
}

// ProgressStart is the initial event every run emits.
var ProgressStart = Progress{Phase: "start", Message: "Starting evaluation…"}

// emitProgress calls fn with p if fn is non-nil. The nil check lets the
// judge package and tests run without a progress sink; the server is the
// only caller that wires one up.
func emitProgress(fn func(Progress), p Progress) {
	if fn != nil {
		fn(p)
	}
}
