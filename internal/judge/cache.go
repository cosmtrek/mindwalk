package judge

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/cosmtrek/mindwalk/internal/model"
)

// Cache persists reports under one file per session key so re-opening a
// session never re-runs the judge. Reports are expensive; traces are not.
type Cache struct {
	Dir string
}

// DefaultCacheDir is ~/.mindwalk/reports — mindwalk's own data directory,
// never inside ~/.claude, ~/.codex, or the inspected repository.
func DefaultCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".mindwalk", "reports")
}

func (c Cache) path(sessionKey string) string {
	return filepath.Join(c.Dir, sessionKey+".json")
}

// Path returns the on-disk location of the session's report file, for
// callers that fingerprint reports without loading them.
func (c Cache) Path(sessionKey string) string {
	return c.path(sessionKey)
}

// Load returns the cached report for the session key, or nil when absent or
// unreadable (a corrupt cache entry is treated as a miss, not an error).
func (c Cache) Load(sessionKey string) *model.Report {
	if c.Dir == "" || sessionKey == "" {
		return nil
	}
	data, err := os.ReadFile(c.path(sessionKey))
	if err != nil {
		return nil
	}
	var report model.Report
	if json.Unmarshal(data, &report) != nil {
		return nil
	}
	// Syntactically valid but hollow payloads ("null", "{}", hand-edited
	// files) must read as a miss: the UI dereferences dimensions and judge
	// unconditionally.
	if report.Version < 1 || len(report.Dimensions) == 0 || report.Judge.CLI == "" {
		return nil
	}
	// A dimension's nil findings serializes as JSON null, which the panel
	// maps over unconditionally; normalize rather than reject. Rubric
	// criteria get the same treatment.
	for i := range report.Dimensions {
		if report.Dimensions[i].Findings == nil {
			report.Dimensions[i].Findings = []model.ReportFinding{}
		}
	}
	if report.Rubric != nil {
		for i := range report.Rubric.Tasks {
			for j := range report.Rubric.Tasks[i].Criteria {
				if report.Rubric.Tasks[i].Criteria[j].Findings == nil {
					report.Rubric.Tasks[i].Criteria[j].Findings = []model.ReportFinding{}
				}
			}
		}
	}
	return &report
}

// FreshAgainstTrace reports whether a cached report still matches the trace
// it would be regenerated from: same prompt version and the same judge input
// digest — event counts alone miss user messages (stored as marks) and
// content edits. The rubric layer is checked against the current task
// evidence separately, because its input window is wider than the scoring
// document's. The judge CLI is deliberately not part of freshness — a valid
// report stays valid. Reports from before the digest existed are stale by
// construction.
func FreshAgainstTrace(report *model.Report, trace *model.Trace) bool {
	if report == nil ||
		report.Judge.PromptVersion != PromptVersion ||
		report.Judge.InputDigest != InputDigest(trace) {
		return false
	}
	return rubricFresh(report, trace)
}

// FreshAgainstSummary is the cheap approximation of FreshAgainstTrace for
// callers holding only a session summary (the list view): it shares the
// prompt-version and digest-presence preconditions but compares the
// summary's event and user-turn counts instead of recomputing the input
// digest, so no trace parse is needed. It may call a report fresh that
// FreshAgainstTrace grades stale (a content edit that keeps both counts);
// the panel's full check corrects that. User turns catch message-only
// growth the event count is blind to.
func FreshAgainstSummary(report *model.Report, meta model.SessionMeta) bool {
	return report != nil &&
		report.Judge.PromptVersion == PromptVersion &&
		report.Judge.InputDigest != "" &&
		report.Session.EventCount == meta.EventCount &&
		report.Session.UserTurns == meta.UserTurns
}

// rubricFresh verifies the rubric layer against the CURRENT task evidence.
// InputDigest reads the scoring document, whose message window is tighter
// than the rubric phase's: a mid-window message revision can leave the
// scoring digest untouched while the task evidence — and therefore the
// rubric that would be regenerated — has changed.
func rubricFresh(report *model.Report, trace *model.Trace) bool {
	rubric := report.Rubric
	if rubric == nil {
		return true
	}
	switch rubric.Status {
	case model.RubricStatusScored:
		if report.Judge.RubricPromptVersion != RubricPromptVersion {
			return false
		}
		// A scored rubric must name a valid source and still fingerprint the
		// task evidence it would be regenerated from.
		if rubric.Source != model.RubricSourceFull && rubric.Source != model.RubricSourceTask {
			return false
		}
		return rubric.TaskDigest == TaskDigest(trace, rubric.Source)
	case model.RubricStatusUnavailable:
		// Deterministic skips stay fresh only while their condition still
		// holds for the current task evidence. no-events needs no recheck
		// (events appearing changes the narrative, which InputDigest covers),
		// and generation-failed deliberately stays fresh — re-running it is
		// the user's explicit call, and RubricSatisfied already refuses to
		// treat it as settled.
		switch rubric.Reason {
		case model.RubricReasonNoTaskText:
			return len(taskMessages(trace.Marks)) == 0
		case model.RubricReasonWeakTaskText:
			return len(taskMessages(trace.Marks)) > 0 && taskTextRunes(trace.Marks) < weakTaskTextRunes
		}
	}
	return true
}

func (c Cache) Store(sessionKey string, report *model.Report) error {
	if c.Dir == "" || sessionKey == "" {
		return nil
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	// A unique temp file per writer: the CLI and the server may finish
	// evaluating the same session concurrently, and a shared name would let
	// them truncate each other mid-write. Last rename wins, atomically.
	tmp, err := os.CreateTemp(c.Dir, sessionKey+"-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), c.path(sessionKey)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
