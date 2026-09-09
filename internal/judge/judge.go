package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cosmtrek/mindwalk/internal/model"
)

// DefaultTimeout bounds one whole evaluation. Two sealed calls (rubric, then
// scoring) at a measured ~30-40s each leave the same headroom the single-call
// pipeline had at five minutes.
const DefaultTimeout = 10 * time.Minute

type Options struct {
	// Runner overrides the subprocess runner; nil selects CLIRunner{CLI, Model}.
	Runner Runner
	// CLI names the judge CLI ("claude" or "codex"); empty auto-detects.
	CLI string
	// Model overrides the CLI's default model; empty keeps the default.
	Model string
	// NoRubric skips the rubric layer entirely: one dimensions-only call,
	// and the report carries no rubric block.
	NoRubric bool
	// CachedReport is the previous report for this session, if any; a scored
	// rubric whose task digest still matches is reused instead of regenerated,
	// so criteria stay stable across re-evaluations.
	CachedReport *model.Report
	// OnProgress, if set, receives a step-level update at each pipeline
	// milestone (rubric skip/reuse/generate, scoring, done/error). The server
	// streams these to the browser via SSE; tests and the CLI leave it nil.
	OnProgress func(Progress)
}

// Analyze runs the judge over one trace and returns the evaluation report.
// The rubric layer resolves first (skip, reuse, or generate-with-degrade);
// scoring is a single unified call covering the four fixed dimensions plus
// any rubric criteria. The judge only contributes findings; verdicts are
// rolled up mechanically. Invalid judge output is retried once before failing.
func Analyze(ctx context.Context, trace *model.Trace, opts Options) (*model.Report, error) {
	runner := opts.Runner
	if runner == nil {
		cli := opts.CLI
		if cli == "" {
			detected, err := DetectCLI()
			if err != nil {
				return nil, err
			}
			cli = detected
		}
		runner = CLIRunner{CLI: cli, Model: opts.Model}
	}

	emitProgress(opts.OnProgress, ProgressStart)

	input := BuildInput(trace)
	var rubric *model.Rubric
	if !opts.NoRubric {
		acquired, err := acquireRubric(ctx, runner, trace, opts.CachedReport, opts.OnProgress)
		if err != nil {
			emitProgress(opts.OnProgress, Progress{Phase: "error", Step: "fail", Message: err.Error()})
			return nil, err
		}
		rubric = acquired
	} else {
		emitProgress(opts.OnProgress, Progress{Phase: "rubric", Step: "skip", Message: "Rubric layer disabled"})
	}
	sysPrompt, scoringInput := prompt, input
	if rubric != nil && rubric.Status == model.RubricStatusScored {
		sysPrompt = scoringPrompt
		scoringInput = "# RUBRIC (data)\n\n" + scoringRubricJSON(rubric) + "\n\n# SESSION\n\n" + input
	}

	emitProgress(opts.OnProgress, Progress{Phase: "scoring", Step: "score", Message: "Scoring session against dimensions and criteria…"})

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		result, err := runner.Run(ctx, sysPrompt, scoringInput)
		if err != nil {
			emitProgress(opts.OnProgress, Progress{Phase: "error", Step: "fail", Message: err.Error()})
			return nil, err
		}
		report, err := parseOutput(result.Text, trace, rubric)
		if err != nil {
			lastErr = err
			emitProgress(opts.OnProgress, Progress{Phase: "scoring", Step: "retry", Message: "Judge output invalid, retrying…"})
			continue
		}
		// Prefer the model the CLI says it used; fall back to what was asked
		// for so the report never silently drops the information.
		judgeModel := result.Model
		if judgeModel == "" {
			judgeModel = opts.Model
		}
		report.Judge = model.ReportJudge{
			CLI:            runner.Name(),
			Model:          judgeModel,
			RequestedModel: opts.Model,
			PromptVersion:  PromptVersion,
			GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
			InputDigest:    InputDigest(trace),
		}
		if report.Rubric != nil && report.Rubric.Status == model.RubricStatusScored {
			report.Judge.RubricPromptVersion = RubricPromptVersion
		}
		emitProgress(opts.OnProgress, Progress{Phase: "done", Step: "complete", Message: "Evaluation complete"})
		return report, nil
	}
	emitProgress(opts.OnProgress, Progress{Phase: "error", Step: "fail", Message: "judge output invalid after retry"})
	return nil, fmt.Errorf("judge output invalid after retry: %w", lastErr)
}

// llmFinding and llmOutput mirror the JSON shapes the scoring prompts request.
type llmFinding struct {
	Claim        string `json:"claim"`
	Severity     string `json:"severity"`
	EvidenceSeqs []int  `json:"evidence_seqs"`
}

type llmOutput struct {
	TaskSummary string `json:"task_summary"`
	Dimensions  []struct {
		Name     string       `json:"name"`
		Findings []llmFinding `json:"findings"`
	} `json:"dimensions"`
	Criteria []struct {
		ID       string       `json:"id"`
		Coverage string       `json:"coverage"`
		Findings []llmFinding `json:"findings"`
	} `json:"criteria"`
	RubricNote     string `json:"rubric_note"`
	NotableMoments []struct {
		Seq  int    `json:"seq"`
		Note string `json:"note"`
	} `json:"notable_moments"`
	Narrative string `json:"narrative"`
}

func parseOutput(raw string, trace *model.Trace, rubric *model.Rubric) (*model.Report, error) {
	payload, err := extractJSON(raw)
	if err != nil {
		return nil, err
	}
	var out llmOutput
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return nil, fmt.Errorf("judge JSON: %w", err)
	}

	validSeqs := make(map[int]bool, len(trace.Events))
	for _, event := range trace.Events {
		validSeqs[event.Seq] = true
	}

	byName := map[string]*model.ReportDimension{}
	for _, dim := range out.Dimensions {
		if !knownDimension(dim.Name) {
			continue
		}
		target, ok := byName[dim.Name]
		if !ok {
			target = &model.ReportDimension{Name: dim.Name, Findings: []model.ReportFinding{}}
			byName[dim.Name] = target
		}
		findings, err := filterFindings(dim.Findings, validSeqs)
		if err != nil {
			return nil, err
		}
		target.Findings = append(target.Findings, findings...)
	}
	if len(byName) != len(model.DimensionNames) {
		return nil, fmt.Errorf("judge output covers %d of %d dimensions", len(byName), len(model.DimensionNames))
	}

	report := &model.Report{
		Version: 1,
		Session: model.ReportSession{
			ID:         trace.Session.ID,
			Harness:    trace.Session.Harness,
			Model:      trace.Session.Model,
			EventCount: trace.Session.EventCount,
			UserTurns:  trace.Stats.UserTurns,
		},
		TaskSummary: out.TaskSummary,
		Narrative:   out.Narrative,
	}
	for _, name := range model.DimensionNames {
		dim := byName[name]
		if len(trace.Events) == 0 {
			// A conversation-only trace has nothing citable: every finding was
			// just dropped, and a good verdict here would be praise on zero
			// evidence. No events, no signal — for all four dimensions.
			dim.Verdict = model.VerdictInsufficientData
		} else {
			dim.Verdict = rollupVerdict(name, dim.Findings, trace.Stats.Observability)
		}
		report.Dimensions = append(report.Dimensions, *dim)
	}
	if rubric != nil {
		if rubric.Status == model.RubricStatusScored {
			scored, err := scoreRubric(rubric, &out, validSeqs)
			if err != nil {
				return nil, err
			}
			report.Rubric = scored
		} else {
			report.Rubric = rubric
		}
	}
	for _, moment := range out.NotableMoments {
		if validSeqs[moment.Seq] && moment.Note != "" {
			report.NotableMoments = append(report.NotableMoments, model.ReportMoment{Seq: moment.Seq, Note: moment.Note})
		}
	}
	return report, nil
}

// filterFindings applies the evidence discipline shared by dimensions and
// rubric criteria: hallucinated seqs are stripped, a finding with no valid
// citation left may not enter the report, and an unrecognized severity
// invalidates the whole output — silently downgrading a misspelled "problem"
// to info would launder a red flag into a good verdict.
func filterFindings(raw []llmFinding, validSeqs map[int]bool) ([]model.ReportFinding, error) {
	findings := make([]model.ReportFinding, 0, len(raw))
	for _, finding := range raw {
		if finding.Claim == "" {
			continue
		}
		seqs := make([]int, 0, len(finding.EvidenceSeqs))
		for _, seq := range finding.EvidenceSeqs {
			if validSeqs[seq] {
				seqs = append(seqs, seq)
			}
		}
		if len(seqs) == 0 {
			continue
		}
		severity, err := normalizeSeverity(finding.Severity)
		if err != nil {
			return nil, err
		}
		findings = append(findings, model.ReportFinding{
			Claim:        finding.Claim,
			Severity:     severity,
			EvidenceSeqs: seqs,
		})
	}
	return findings, nil
}

// scoreRubric merges the scoring output into a copy of the rubric. The judge
// echoes a flat criteria list; grouping comes from the rubric itself, so a
// criterion can never land in the wrong task. Every rubric criterion must be
// scored — a missing one invalidates the output; unknown and duplicate ids
// are dropped.
func scoreRubric(rubric *model.Rubric, out *llmOutput, validSeqs map[int]bool) (*model.Rubric, error) {
	type score struct {
		coverage string
		findings []model.ReportFinding
	}
	expected := map[string]bool{}
	for _, task := range rubric.Tasks {
		for _, criterion := range task.Criteria {
			expected[criterion.ID] = true
		}
	}
	scores := map[string]score{}
	for _, criterion := range out.Criteria {
		// Unknown ids are dropped before any validation: an invented entry is
		// noise the contract discards, and its malformed coverage or findings
		// must not be able to fail the whole scoring pass.
		if !expected[criterion.ID] {
			continue
		}
		if _, dup := scores[criterion.ID]; dup {
			continue
		}
		coverage, err := normalizeCoverage(criterion.Coverage)
		if err != nil {
			return nil, err
		}
		findings, err := filterFindings(criterion.Findings, validSeqs)
		if err != nil {
			return nil, err
		}
		scores[criterion.ID] = score{coverage: coverage, findings: findings}
	}

	scored := &model.Rubric{
		Status:     rubric.Status,
		Source:     rubric.Source,
		TaskDigest: rubric.TaskDigest,
		Note:       truncateRunes(strings.TrimSpace(out.RubricNote), maxRubricTextRunes),
		Tasks:      make([]model.RubricTask, len(rubric.Tasks)),
	}
	for i, task := range rubric.Tasks {
		copied := task
		copied.Criteria = make([]model.RubricCriterion, len(task.Criteria))
		for j, criterion := range task.Criteria {
			result, ok := scores[criterion.ID]
			if !ok {
				return nil, fmt.Errorf("judge output misses rubric criterion %q", criterion.ID)
			}
			criterion.Coverage = result.coverage
			criterion.Findings = result.findings
			criterion.Verdict = rollupCriterion(result.coverage, result.findings)
			copied.Criteria[j] = criterion
		}
		scored.Tasks[i] = copied
	}
	return scored, nil
}

// rollupVerdict derives the dimension verdict from finding severities; the
// judge never decides verdicts. Blind spots recorded by the deterministic
// layer force insufficient-data regardless of what the judge observed.
func rollupVerdict(name string, findings []model.ReportFinding, obs model.Observability) string {
	if obs.Reads == model.ObservabilityUnavailable && (name == model.DimensionExploration || name == model.DimensionWandering) {
		return model.VerdictInsufficientData
	}
	if obs.Errors == model.ObservabilityUnavailable && name == model.DimensionVerification {
		return model.VerdictInsufficientData
	}
	return rollupSeverities(findings)
}

// rollupCriterion is the rubric-layer analogue: the coverage grade plays the
// role observability plays for dimensions — a criterion the log cannot
// evidence reads as insufficient data, never as a flaw.
func rollupCriterion(coverage string, findings []model.ReportFinding) string {
	if coverage == model.CoverageNone {
		return model.VerdictInsufficientData
	}
	return rollupSeverities(findings)
}

func rollupSeverities(findings []model.ReportFinding) string {
	verdict := model.VerdictGood
	for _, finding := range findings {
		switch finding.Severity {
		case model.SeverityProblem:
			return model.VerdictProblem
		case model.SeverityWarning:
			verdict = model.VerdictWarning
		}
	}
	return verdict
}

func knownDimension(name string) bool {
	for _, known := range model.DimensionNames {
		if name == known {
			return true
		}
	}
	return false
}

// normalizeSeverity forgives casing and whitespace but nothing else: an
// unrecognized severity is judge output we cannot trust to aggregate.
func normalizeSeverity(severity string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case model.SeverityInfo:
		return model.SeverityInfo, nil
	case model.SeverityWarning:
		return model.SeverityWarning, nil
	case model.SeverityProblem:
		return model.SeverityProblem, nil
	default:
		return "", fmt.Errorf("judge output: unknown severity %q", severity)
	}
}

// normalizeCoverage applies the same strictness to coverage: an unknown grade
// could silently flip a criterion between scored and insufficient-data.
func normalizeCoverage(coverage string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(coverage)) {
	case model.CoverageSufficient:
		return model.CoverageSufficient, nil
	case model.CoveragePartial:
		return model.CoveragePartial, nil
	case model.CoverageNone:
		return model.CoverageNone, nil
	default:
		return "", fmt.Errorf("judge output: unknown coverage %q", coverage)
	}
}

// extractJSON returns the first balanced top-level JSON object in text,
// tolerating judge CLIs that wrap output in logs or markdown fences.
func extractJSON(text string) (string, error) {
	start := -1
	depth := 0
	inString := false
	escaped := false
	for i, r := range text {
		if start == -1 {
			if r == '{' {
				start = i
				depth = 1
			}
			continue
		}
		if escaped {
			escaped = false
			continue
		}
		switch r {
		case '\\':
			if inString {
				escaped = true
			}
		case '"':
			inString = !inString
		case '{':
			if !inString {
				depth++
			}
		case '}':
			if !inString {
				depth--
				if depth == 0 {
					return text[start : i+1], nil
				}
			}
		}
	}
	return "", fmt.Errorf("no JSON object in judge output")
}
