// Package review derives deterministic owner-review and comparison read
// models from redacted normalized traces. It never reads provider logs.
package review

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/agents"
	"github.com/cosmtrek/mindwalk/internal/event"
	"github.com/cosmtrek/mindwalk/internal/model"
)

const SchemaVersion = 1

type SessionReview struct {
	SchemaVersion        int              `json:"schemaVersion"`
	SessionID            string           `json:"sessionId"`
	Files                []string         `json:"files"`
	EditedFiles          []string         `json:"editedFiles"`
	Errors               int              `json:"errors"`
	Verifications        int              `json:"verifications"`
	ChurnFiles           []string         `json:"churnFiles"`
	ScopeDriftTouches    int              `json:"scopeDriftTouches"`
	EditsAfterLastVerify int              `json:"editsAfterLastVerify"`
	AgentProcesses       int              `json:"agentProcesses"`
	Flags                []string         `json:"flags"`
	Provenance           event.Provenance `json:"provenance"`
}

type Comparison struct {
	SchemaVersion int              `json:"schemaVersion"`
	Left          SessionReview    `json:"left"`
	Right         SessionReview    `json:"right"`
	SharedFiles   []string         `json:"sharedFiles"`
	OnlyLeft      []string         `json:"onlyLeft"`
	OnlyRight     []string         `json:"onlyRight"`
	MemoryStatus  string           `json:"memoryStatus"`
	MemoryNote    string           `json:"memoryNote"`
	Provenance    event.Provenance `json:"provenance"`
}

func Analyze(trace *model.Trace, processes []agents.Process) SessionReview {
	review := SessionReview{SchemaVersion: SchemaVersion, AgentProcesses: len(processes), Provenance: derived("session-review")}
	if trace == nil {
		review.SessionID = "UNKNOWN"
		review.Flags = []string{"trace unavailable"}
		return review
	}
	review.SessionID = trace.Session.ID
	files := map[string]bool{}
	edited := map[string]int{}
	lastVerify := -1
	for _, item := range trace.Events {
		if item.IsError {
			review.Errors++
		}
		if item.Action == "verify" {
			review.Verifications++
			lastVerify = item.Seq
		}
		review.ScopeDriftTouches += len(item.Outside)
		for _, target := range item.Targets {
			if target.Path == "" {
				continue
			}
			files[target.Path] = true
			if target.Touch == "edit" {
				edited[target.Path]++
			}
		}
	}
	for path := range files {
		review.Files = append(review.Files, path)
	}
	for path, count := range edited {
		review.EditedFiles = append(review.EditedFiles, path)
		if count >= 3 {
			review.ChurnFiles = append(review.ChurnFiles, path)
		}
	}
	for _, item := range trace.Events {
		if item.Seq > lastVerify && item.Action == "edit" {
			review.EditsAfterLastVerify++
		}
	}
	sort.Strings(review.Files)
	sort.Strings(review.EditedFiles)
	sort.Strings(review.ChurnFiles)
	if review.Errors > 0 {
		review.Flags = append(review.Flags, fmt.Sprintf("%d observed error(s)", review.Errors))
	}
	if len(review.ChurnFiles) > 0 {
		review.Flags = append(review.Flags, fmt.Sprintf("%d churn file(s)", len(review.ChurnFiles)))
	}
	if review.ScopeDriftTouches > 0 {
		review.Flags = append(review.Flags, fmt.Sprintf("%d outside-repository touch(es)", review.ScopeDriftTouches))
	}
	if review.EditsAfterLastVerify > 0 {
		review.Flags = append(review.Flags, fmt.Sprintf("%d edit(s) after last verification", review.EditsAfterLastVerify))
	}
	if len(review.Flags) == 0 {
		review.Flags = []string{"no review flags in supported signals"}
	}
	return review
}

func Compare(leftTrace, rightTrace *model.Trace, leftAgents, rightAgents []agents.Process) Comparison {
	left := Analyze(leftTrace, leftAgents)
	right := Analyze(rightTrace, rightAgents)
	comparison := Comparison{
		SchemaVersion: SchemaVersion, Left: left, Right: right,
		SharedFiles:  []string{},
		OnlyLeft:     []string{},
		OnlyRight:    []string{},
		MemoryStatus: "UNAVAILABLE",
		MemoryNote:   "no canonical session-to-memory relationship is recorded",
		Provenance:   derived("session-comparison"),
	}
	leftSet := toSet(left.Files)
	rightSet := toSet(right.Files)
	for path := range leftSet {
		if rightSet[path] {
			comparison.SharedFiles = append(comparison.SharedFiles, path)
		} else {
			comparison.OnlyLeft = append(comparison.OnlyLeft, path)
		}
	}
	for path := range rightSet {
		if !leftSet[path] {
			comparison.OnlyRight = append(comparison.OnlyRight, path)
		}
	}
	sort.Strings(comparison.SharedFiles)
	sort.Strings(comparison.OnlyLeft)
	sort.Strings(comparison.OnlyRight)
	return comparison
}

func Markdown(review SessionReview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Mindwalk owner review: %s\n\n", markdownText(review.SessionID))
	fmt.Fprintf(&b, "- Files observed: %d\n- Edited files: %d\n- Errors: %d\n- Verifications: %d\n- Outside-repository touches: %d\n- Edits after last verification: %d\n- Agent processes: %d\n\n", len(review.Files), len(review.EditedFiles), review.Errors, review.Verifications, review.ScopeDriftTouches, review.EditsAfterLastVerify, review.AgentProcesses)
	b.WriteString("## Review flags\n\n")
	for _, flag := range review.Flags {
		fmt.Fprintf(&b, "- %s\n", markdownText(flag))
	}
	b.WriteString("\n## Files\n\n")
	for _, path := range review.Files {
		fmt.Fprintf(&b, "- `%s`\n", markdownText(path))
	}
	b.WriteString("\nProvenance: derived from redacted normalized trace and canonical event projections.\n")
	return b.String()
}

func derived(sourceID string) event.Provenance {
	confidence := float64(1)
	return event.Provenance{SourceType: "derived-read-model", SourceName: "mindwalk-review", SourceEventID: &sourceID, Quality: event.QualityDerived, Confidence: &confidence, Explanation: "computed deterministically from redacted normalized trace metadata"}
}

func toSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func markdownText(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "`", "'")
	return value
}
