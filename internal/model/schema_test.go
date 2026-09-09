package model

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// validateAgainstSchema marshals v to JSON, compiles the named schema file,
// and reports a fatal error if the JSON does not validate.
func validateAgainstSchema(t *testing.T, schemaPath string, v any) {
	t.Helper()

	document, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		t.Fatal(err)
	}

	compiler := jsonschema.NewCompiler()

	schema, err := compiler.Compile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := schema.Validate(value); err != nil {
		t.Fatalf("%s schema validation failed: %v\nJSON: %s", schemaPath, err, document)
	}
}

func TestTraceSchemaAcceptsRepresentativeTrace(t *testing.T) {
	tr := Trace{
		Version: 1,
		Session: TraceSession{
			ID:         "test-session",
			Harness:    "crush",
			Model:      "claude-sonnet-4",
			EventCount: 3,
		},
		Events: []Event{
			{
				Seq:     1,
				Tool:    "read",
				Action:  "read",
				Summary: "read main.go",
				Targets: []Target{{Path: "main.go", Touch: "read"}},
			},
			{
				Seq:     2,
				Tool:    "edit",
				Action:  "edit",
				Summary: "edit utils.go",
				Targets: []Target{{Path: "util.go", Touch: "edit"}},
			},
			{Seq: 3, Tool: "bash", Action: "exec", Summary: "go test", Targets: []Target{}},
		},
		Marks: []Mark{
			{Seq: 1, Type: "user-message"},
			{Seq: 2, Type: "thinking", Duration: 5},
		},
		Stats: Stats{
			FilesInRepo:           10,
			Fovea:                 3,
			Parafovea:             2,
			Edited:                1,
			EventsBeforeFirstEdit: 1,
			RegressionRate:        0.0,
			ErrorRate:             0.33,
			Actions:               ActionCounts{Read: 1, Edit: 1, Exec: 1, Other: 0},
			Errors:                ActionCounts{},
			Observability:         Observability{Reads: "exact", Errors: "estimated"},
			ResultBytes:           1024,
			UserTurns:             1,
		},
	}
	validateAgainstSchema(t, "../../schema/trace.schema.json", tr)
}

func TestReportSchemaAcceptsRepresentativeReport(t *testing.T) {
	rep := Report{
		Version: 1,
		Session: ReportSession{
			ID:         "test-session",
			Harness:    "crush",
			Model:      "claude-sonnet-4",
			EventCount: 10,
			UserTurns:  2,
		},
		Judge: ReportJudge{
			CLI:           "claude",
			Model:         "claude-sonnet-4",
			PromptVersion: 1,
			GeneratedAt:   "2026-08-04T12:00:00Z",
			InputDigest:   "abc123",
		},
		TaskSummary: "Fix a bug in the parser",
		Dimensions: []ReportDimension{
			{
				Name:    DimensionExploration,
				Verdict: VerdictGood,
				Findings: []ReportFinding{
					{Claim: "Explored the right files", Severity: SeverityInfo, EvidenceSeqs: []int{1, 2}},
				},
			},
			{
				Name:    DimensionScope,
				Verdict: VerdictWarning,
				Findings: []ReportFinding{
					{Claim: "Edited an unrelated file", Severity: SeverityWarning, EvidenceSeqs: []int{5}},
				},
			},
			{
				Name:    DimensionWandering,
				Verdict: VerdictGood,
				Findings: []ReportFinding{
					{Claim: "Stayed focused", Severity: SeverityInfo, EvidenceSeqs: []int{3}},
				},
			},
			{
				Name:    DimensionVerification,
				Verdict: VerdictProblem,
				Findings: []ReportFinding{
					{Claim: "No verification step", Severity: SeverityProblem, EvidenceSeqs: []int{8}},
				},
			},
		},
		Narrative: "The agent explored well but skipped verification.",
	}
	validateAgainstSchema(t, "../../schema/report.schema.json", rep)
}

func TestReportSchemaAcceptsReportWithRubric(t *testing.T) {
	rep := Report{
		Version: 1,
		Session: ReportSession{
			ID:         "rubric-test",
			Harness:    "codex",
			EventCount: 5,
		},
		Judge: ReportJudge{
			CLI:                 "codex",
			PromptVersion:       1,
			RubricPromptVersion: 1,
			GeneratedAt:         "2026-08-04T12:00:00Z",
			InputDigest:         "def456",
		},
		TaskSummary: "Add a feature",
		Dimensions: []ReportDimension{
			{
				Name:    DimensionExploration,
				Verdict: VerdictGood,
				Findings: []ReportFinding{
					{Claim: "good", Severity: SeverityInfo, EvidenceSeqs: []int{1}},
				},
			},
			{
				Name:     DimensionScope,
				Verdict:  VerdictGood,
				Findings: []ReportFinding{{Claim: "good", Severity: SeverityInfo, EvidenceSeqs: []int{1}}},
			},
			{
				Name:     DimensionWandering,
				Verdict:  VerdictGood,
				Findings: []ReportFinding{{Claim: "good", Severity: SeverityInfo, EvidenceSeqs: []int{1}}},
			},
			{
				Name:     DimensionVerification,
				Verdict:  VerdictGood,
				Findings: []ReportFinding{{Claim: "good", Severity: SeverityInfo, EvidenceSeqs: []int{1}}},
			},
		},
		Rubric: &Rubric{
			Status: RubricStatusScored,
			Tasks: []RubricTask{
				{
					Title:              "Add the feature",
					AnchorUserMessages: []int{1},
					Criteria: []RubricCriterion{
						{
							ID:       "crit-1",
							Title:    "Implementation correct",
							Coverage: CoverageSufficient,
							Verdict:  VerdictGood,
							Findings: []ReportFinding{
								{Claim: "correct", Severity: SeverityInfo, EvidenceSeqs: []int{1}},
							},
						},
					},
				},
			},
		},
		Narrative: "Done.",
	}
	validateAgainstSchema(t, "../../schema/report.schema.json", rep)
}

func TestCityMapSchemaAcceptsRepresentativeCityMap(t *testing.T) {
	cm := CityMap{
		Version: 1,
		Repo: RepoMeta{
			Root:        "/tmp/repo",
			Commit:      "abc123",
			Dirty:       false,
			GeneratedAt: "2026-08-04T12:00:00Z",
		},
		Files: []CityFile{
			{
				ID:    0,
				Path:  "main.go",
				Dir:   "",
				Lines: 100,
				Bytes: 2048,
				Lang:  "go",
				Rect:  Rect{X: 0, Z: 0, W: 0.5, D: 0.5},
			},
			{
				ID:    1,
				Path:  "util.go",
				Dir:   "",
				Lines: 50,
				Bytes: 1024,
				Lang:  "go",
				Rect:  Rect{X: 0.5, Z: 0, W: 0.5, D: 0.5},
			},
		},
		Dirs: []CityDir{
			{Path: "pkg", Depth: 1, Rect: Rect{X: 0, Z: 0, W: 1, D: 1}, FileCount: 2, Lines: 150},
		},
		Layout: LayoutMeta{Algorithm: "squarified", Weight: "lines"},
	}
	validateAgainstSchema(t, "../../schema/citymap.schema.json", cm)
}
