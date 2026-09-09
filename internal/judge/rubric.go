package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/model"
)

// Rubric shape bounds. The prompt asks for less (4-6 single-task, ≤10 total);
// validation accepts a margin, and anything beyond it invalidates the output.
// The byte cap bounds both the injection surface a hostile trace gets to
// shape and what the panel is asked to render.
const (
	maxRubricTasks      = 6
	maxCriteriaPerTask  = 6
	minRubricCriteria   = 3
	maxRubricCriteria   = 12
	maxCriterionIDLen   = 48
	maxRubricTitleRunes = 80
	maxRubricTextRunes  = 500
	maxRubricJSONBytes  = 12 * 1024
	// weakTaskTextRunes is the floor under which user messages carry too
	// little task signal to derive criteria from ("continue", "ok" sessions).
	// Initial guess — calibrated against historical sessions in M1.5.
	weakTaskTextRunes = 30
)

var criterionIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// llmRubric mirrors the JSON shape rubricPrompt requests.
type llmRubric struct {
	Tasks []struct {
		Title              string `json:"title"`
		Type               string `json:"type"`
		AnchorUserMessages []int  `json:"anchor_user_messages"`
		Criteria           []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Why   string `json:"why"`
			Good  string `json:"good"`
			Bad   string `json:"bad"`
		} `json:"criteria"`
	} `json:"tasks"`
}

// acquireRubric resolves the rubric layer for one run: a deterministic skip,
// a reuse of the cached report's rubric, or up to two generation attempts.
// Generation failure degrades (status unavailable) rather than erroring —
// the fixed dimensions must never be blocked by the rubric layer. Only a
// subprocess failure is a hard error, since scoring would hit it too.
func acquireRubric(ctx context.Context, runner Runner, trace *model.Trace, cached *model.Report, onProgress func(Progress)) (*model.Rubric, error) {
	// Conversation-only sessions (no tool events) leave nothing to cite:
	// scoring would drop every finding and hand out good verdicts on zero
	// evidence — the M1.5 bench caught exactly that.
	if len(trace.Events) == 0 {
		emitProgress(onProgress, Progress{Phase: "rubric", Step: "skip", Message: "No tool events — skipping rubric"})
		return &model.Rubric{Status: model.RubricStatusUnavailable, Reason: model.RubricReasonNoEvents}, nil
	}
	// One task-evidence contract: the generator's input, the anchor
	// validation set, the digest, and the weak-text gate all read
	// taskMessages — never a differently budgeted list.
	messages := taskMessages(trace.Marks)
	if len(messages) == 0 {
		emitProgress(onProgress, Progress{Phase: "rubric", Step: "skip", Message: "No task text — skipping rubric"})
		return &model.Rubric{Status: model.RubricStatusUnavailable, Reason: model.RubricReasonNoTaskText}, nil
	}
	if taskTextRunes(trace.Marks) < weakTaskTextRunes {
		emitProgress(onProgress, Progress{Phase: "rubric", Step: "skip", Message: "Task text too short — skipping rubric"})
		return &model.Rubric{Status: model.RubricStatusUnavailable, Reason: model.RubricReasonWeakTaskText}, nil
	}
	digest := TaskDigest(trace, model.RubricSourceFull)
	if reused := reusableRubric(cached, digest); reused != nil {
		emitProgress(onProgress, Progress{Phase: "rubric", Step: "reuse", Message: "Reusing cached rubric criteria"})
		return reused, nil
	}
	emitProgress(onProgress, Progress{Phase: "rubric", Step: "generate", Message: "Drafting task-specific criteria…"})
	input := BuildRubricInput(trace)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := runner.Run(ctx, rubricPrompt, input)
		if err != nil {
			return nil, err
		}
		tasks, err := parseRubric(result.Text, messages)
		if err != nil {
			continue
		}
		emitProgress(onProgress, Progress{Phase: "rubric", Step: "complete", Message: "Rubric criteria drafted"})
		return &model.Rubric{
			Status:     model.RubricStatusScored,
			Source:     model.RubricSourceFull,
			TaskDigest: digest,
			Tasks:      tasks,
		}, nil
	}
	emitProgress(onProgress, Progress{Phase: "rubric", Step: "fail", Message: "Rubric generation failed — scoring dimensions only"})
	return &model.Rubric{Status: model.RubricStatusUnavailable, Reason: model.RubricReasonGenerationFailed}, nil
}

// reusableRubric lifts the cached report's rubric when the task wording and
// rubric prompt are unchanged: criteria stay stable across re-evaluations,
// and the run saves the generation call. Scores are stripped — the new
// scoring pass owns them.
func reusableRubric(cached *model.Report, digest string) *model.Rubric {
	if cached == nil || cached.Rubric == nil ||
		cached.Rubric.Status != model.RubricStatusScored ||
		cached.Rubric.TaskDigest != digest ||
		cached.Judge.RubricPromptVersion != RubricPromptVersion {
		return nil
	}
	tasks := make([]model.RubricTask, len(cached.Rubric.Tasks))
	for i, task := range cached.Rubric.Tasks {
		copied := task
		copied.AnchorUserMessages = append([]int(nil), task.AnchorUserMessages...)
		copied.AnchorSeqs = append([]int(nil), task.AnchorSeqs...)
		copied.Criteria = make([]model.RubricCriterion, len(task.Criteria))
		for j, criterion := range task.Criteria {
			copied.Criteria[j] = model.RubricCriterion{
				ID:    criterion.ID,
				Title: criterion.Title,
				Why:   criterion.Why,
				Good:  criterion.Good,
				Bad:   criterion.Bad,
			}
		}
		tasks[i] = copied
	}
	return &model.Rubric{
		Status:     model.RubricStatusScored,
		Source:     cached.Rubric.Source,
		TaskDigest: digest,
		Tasks:      tasks,
	}
}

// parseRubric validates one generation attempt against the shape bounds and
// the session's real user messages, and derives anchor seqs. Any violation
// invalidates the whole output — a rubric is trusted downstream (it goes
// into the scoring prompt and the report), so nothing malformed may pass.
func parseRubric(raw string, messages []userMessage) ([]model.RubricTask, error) {
	payload, err := extractJSON(raw)
	if err != nil {
		return nil, err
	}
	if len(payload) > maxRubricJSONBytes {
		return nil, fmt.Errorf("rubric: %d bytes exceeds the %d cap", len(payload), maxRubricJSONBytes)
	}
	var out llmRubric
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return nil, fmt.Errorf("rubric JSON: %w", err)
	}
	if len(out.Tasks) == 0 || len(out.Tasks) > maxRubricTasks {
		return nil, fmt.Errorf("rubric: %d tasks, want 1-%d", len(out.Tasks), maxRubricTasks)
	}

	seqByOrdinal := make(map[int]int, len(messages))
	for _, message := range messages {
		seqByOrdinal[message.ordinal] = message.seq
	}
	seenOrdinals := map[int]bool{}
	seenIDs := map[string]bool{}
	totalCriteria := 0
	tasks := make([]model.RubricTask, 0, len(out.Tasks))
	for _, task := range out.Tasks {
		title := strings.TrimSpace(task.Title)
		if title == "" || len([]rune(title)) > maxRubricTitleRunes {
			return nil, fmt.Errorf("rubric: bad task title %q", task.Title)
		}
		if len(task.AnchorUserMessages) == 0 {
			return nil, fmt.Errorf("rubric: task %q has no anchor user messages", title)
		}
		anchors := append([]int(nil), task.AnchorUserMessages...)
		sort.Ints(anchors)
		seqs := make([]int, 0, len(anchors))
		for _, ordinal := range anchors {
			seq, ok := seqByOrdinal[ordinal]
			if !ok {
				return nil, fmt.Errorf("rubric: task %q anchors unknown user message #%d", title, ordinal)
			}
			if seenOrdinals[ordinal] {
				return nil, fmt.Errorf("rubric: user message #%d anchored by more than one task", ordinal)
			}
			seenOrdinals[ordinal] = true
			if len(seqs) == 0 || seqs[len(seqs)-1] != seq {
				seqs = append(seqs, seq)
			}
		}
		if len(task.Criteria) == 0 || len(task.Criteria) > maxCriteriaPerTask {
			return nil, fmt.Errorf("rubric: task %q has %d criteria, want 1-%d", title, len(task.Criteria), maxCriteriaPerTask)
		}
		criteria := make([]model.RubricCriterion, 0, len(task.Criteria))
		for _, criterion := range task.Criteria {
			id := strings.TrimSpace(criterion.ID)
			if len(id) > maxCriterionIDLen || !criterionIDPattern.MatchString(id) {
				return nil, fmt.Errorf("rubric: bad criterion id %q", criterion.ID)
			}
			if seenIDs[id] {
				return nil, fmt.Errorf("rubric: duplicate criterion id %q", id)
			}
			seenIDs[id] = true
			ctitle := strings.TrimSpace(criterion.Title)
			if ctitle == "" || len([]rune(ctitle)) > maxRubricTitleRunes {
				return nil, fmt.Errorf("rubric: bad title for criterion %q", id)
			}
			for _, text := range []string{criterion.Why, criterion.Good, criterion.Bad} {
				if len([]rune(text)) > maxRubricTextRunes {
					return nil, fmt.Errorf("rubric: overlong text on criterion %q", id)
				}
			}
			criteria = append(criteria, model.RubricCriterion{
				ID:    id,
				Title: ctitle,
				Why:   strings.TrimSpace(criterion.Why),
				Good:  strings.TrimSpace(criterion.Good),
				Bad:   strings.TrimSpace(criterion.Bad),
			})
		}
		totalCriteria += len(criteria)
		tasks = append(tasks, model.RubricTask{
			Title:              title,
			Type:               strings.TrimSpace(strings.ToLower(task.Type)),
			AnchorUserMessages: anchors,
			AnchorSeqs:         seqs,
			Criteria:           criteria,
		})
	}
	if totalCriteria < minRubricCriteria || totalCriteria > maxRubricCriteria {
		return nil, fmt.Errorf("rubric: %d criteria total, want %d-%d", totalCriteria, minRubricCriteria, maxRubricCriteria)
	}
	return tasks, nil
}

// scoringRubricJSON renders the rubric as the data section of the scoring
// input: criteria definitions only — anchors and digests are bookkeeping the
// scorer has no use for.
func scoringRubricJSON(rubric *model.Rubric) string {
	type criterion struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Why   string `json:"why,omitempty"`
		Good  string `json:"good,omitempty"`
		Bad   string `json:"bad,omitempty"`
	}
	type task struct {
		Title    string      `json:"title"`
		Type     string      `json:"type,omitempty"`
		Criteria []criterion `json:"criteria"`
	}
	tasks := make([]task, 0, len(rubric.Tasks))
	for _, t := range rubric.Tasks {
		out := task{Title: t.Title, Type: t.Type}
		for _, c := range t.Criteria {
			out.Criteria = append(out.Criteria, criterion{ID: c.ID, Title: c.Title, Why: c.Why, Good: c.Good, Bad: c.Bad})
		}
		tasks = append(tasks, out)
	}
	encoded, err := json.Marshal(map[string]any{"tasks": tasks})
	if err != nil {
		return `{"tasks":[]}`
	}
	return string(encoded)
}

// RubricSatisfied reports whether a cached report already answers a
// rubric-enabled request: it carries a scored rubric, or records a
// deterministic skip a re-run would only repeat. generation-failed is
// transient and worth a fresh run.
func RubricSatisfied(report *model.Report) bool {
	if report == nil || report.Rubric == nil {
		return false
	}
	if report.Rubric.Status == model.RubricStatusScored {
		return true
	}
	return report.Rubric.Reason == model.RubricReasonNoTaskText ||
		report.Rubric.Reason == model.RubricReasonWeakTaskText ||
		report.Rubric.Reason == model.RubricReasonNoEvents
}
