package model

// ComputeStats derives session facts from a parsed trace. errorSignal is the
// adapter's grade for its own error detection (ObservabilityExact when the
// source log flags failures structurally, ObservabilityEstimated when they
// are inferred from output text); an empty value falls back to estimated.
func ComputeStats(trace *Trace, filesInRepo int, errorSignal string) Stats {
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
	// Weak read targets are inferred from command text, so any of them in the
	// mix downgrades the re-read rate; no reads at all leaves it undefined.
	switch {
	case readEvents == 0:
		stats.Observability.Reads = ObservabilityUnavailable
	case weakReads == 0:
		stats.Observability.Reads = ObservabilityExact
	default:
		stats.Observability.Reads = ObservabilityEstimated
	}
	if errorSignal == "" {
		errorSignal = ObservabilityEstimated
	}
	if errorSignal == ObservabilityExact && unknownOutcomes {
		errorSignal = ObservabilityEstimated
	}
	stats.Observability.Errors = errorSignal
	return stats
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
