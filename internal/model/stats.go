package model

// ObservabilitySignals carries adapter-supplied observability grades into
// ComputeStats, preventing positional-parameter explosion as more signals
// are added. Each field overrides the derived grade when non-empty.
type ObservabilitySignals struct {
	// Errors is the adapter's grade for its own error detection
	// (ObservabilityExact when the source log flags failures structurally,
	// ObservabilityEstimated when they are inferred from output text).
	// Empty falls back to estimated.
	Errors string
	// Reads overrides the reads grade when non-empty (used by adapters
	// that have a structural read_files table); empty falls back to
	// deriving from weak target flags.
	Reads string
}

// ComputeStats derives session facts from a parsed trace. The signals
// parameter carries adapter-supplied observability grades; empty fields
// are derived from the trace data.
func ComputeStats(trace *Trace, filesInRepo int, signals ObservabilitySignals) Stats {
	state := map[string]string{}
	lastReadVersion := map[string]int{}
	editVersion := map[string]int{}
	readEvents := 0
	weakReads := 0
	repeatedReads := 0
	errors := 0
	unknownOutcomes := false
	firstEdit := -1

	stats := Stats{FilesInRepo: filesInRepo}

	for _, event := range trace.Events {
		countAction(&stats.Actions, event.Action)
		if event.IsError {
			errors++
			countAction(&stats.Errors, event.Action)
		} else if !event.OutcomeKnown {
			unknownOutcomes = true
		}
		stats.ResultBytes += int64(event.ResultBytes)
		switch event.Action {
		case "verify":
			stats.EditsAfterLastVerify = 0
		case "edit":
			stats.EditsAfterLastVerify++
		}
		for _, target := range event.Targets {
			if target.Path == "" {
				continue
			}
			prev := state[target.Path]
			if RankTouch(target.Touch) > RankTouch(prev) {
				state[target.Path] = target.Touch
			}
			if target.Touch == "edit" {
				editVersion[target.Path]++
			}
			if target.Touch == "read" {
				readEvents++
				if target.Weak {
					weakReads++
				}
				if version, ok := lastReadVersion[target.Path]; ok && version == editVersion[target.Path] {
					repeatedReads++
				}
				lastReadVersion[target.Path] = editVersion[target.Path]
			}
			if target.Touch == "edit" && firstEdit == -1 {
				firstEdit = event.Seq
			}
		}
	}

	if firstEdit >= 0 {
		stats.EventsBeforeFirstEdit = firstEdit
	} else {
		stats.EventsBeforeFirstEdit = len(trace.Events)
	}

	for _, touch := range state {
		switch touch {
		case "edit":
			stats.Edited++
			stats.Fovea++
		case "read":
			stats.Fovea++
		case "hit":
			stats.Parafovea++
		}
	}
	for _, count := range editVersion {
		if count > stats.MaxEditsPerFile {
			stats.MaxEditsPerFile = count
		}
		if count >= 3 {
			stats.ChurnFiles++
		}
	}
	for _, mark := range trace.Marks {
		switch mark.Type {
		case "user-message":
			stats.UserTurns++
		case "compaction":
			stats.Compactions++
		case "subagent":
			stats.Subagents++
		}
	}
	if readEvents > 0 {
		stats.RegressionRate = float64(repeatedReads) / float64(readEvents)
	}
	if len(trace.Events) > 0 {
		stats.ErrorRate = float64(errors) / float64(len(trace.Events))
	}
	// The reads grade: prefer the adapter-supplied signal (structural
	// truth from a read_files table); fall back to deriving from weak
	// target flags when the adapter did not supply one. Unrecognized
	// override values are treated as absent so adapter bugs can never
	// leak an invalid grade into stats or the exported schema.
	readsSignal := signals.Reads
	if !knownObservabilityGrade(readsSignal) {
		switch {
		case readEvents == 0:
			readsSignal = ObservabilityUnavailable
		case weakReads == 0:
			readsSignal = ObservabilityExact
		default:
			readsSignal = ObservabilityEstimated
		}
	}
	stats.Observability.Reads = readsSignal
	errorSignal := signals.Errors
	if !knownObservabilityGrade(errorSignal) {
		errorSignal = ObservabilityEstimated
	}

	if errorSignal == ObservabilityExact && unknownOutcomes {
		errorSignal = ObservabilityEstimated
	}

	stats.Observability.Errors = errorSignal
	return stats
}

func knownObservabilityGrade(v string) bool {
	switch v {
	case ObservabilityExact, ObservabilityEstimated, ObservabilityUnavailable:
		return true
	default:
		return false
	}
}

func countAction(counts *ActionCounts, action string) {
	switch action {
	case "search":
		counts.Search++
	case "read":
		counts.Read++
	case "edit":
		counts.Edit++
	case "exec":
		counts.Exec++
	case "verify":
		counts.Verify++
	default:
		counts.Other++
	}
}

func RankTouch(touch string) int {
	switch touch {
	case "edit":
		return 3
	case "read":
		return 2
	case "hit":
		return 1
	default:
		return 0
	}
}
