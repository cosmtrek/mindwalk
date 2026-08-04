package judge

import (
	"testing"

	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestFreshAgainstSummary(t *testing.T) {
	report := &model.Report{
		Session: model.ReportSession{EventCount: 3, UserTurns: 2},
		Judge:   model.ReportJudge{PromptVersion: PromptVersion, InputDigest: "d"},
	}
	meta := model.SessionMeta{EventCount: 3, UserTurns: 2}
	if !FreshAgainstSummary(report, meta) {
		t.Fatal("matching counts and current prompt graded stale")
	}
	if FreshAgainstSummary(report, model.SessionMeta{EventCount: 4, UserTurns: 2}) {
		t.Fatal("event growth graded fresh")
	}
	if FreshAgainstSummary(report, model.SessionMeta{EventCount: 3, UserTurns: 3}) {
		t.Fatal("message-only growth graded fresh")
	}
	noDigest := *report
	noDigest.Judge.InputDigest = ""
	if FreshAgainstSummary(&noDigest, meta) {
		t.Fatal("pre-digest report graded fresh; the panel would disagree")
	}
	oldPrompt := *report
	oldPrompt.Judge.PromptVersion = PromptVersion - 1
	if FreshAgainstSummary(&oldPrompt, meta) {
		t.Fatal("outdated prompt graded fresh")
	}
	if FreshAgainstSummary(nil, meta) {
		t.Fatal("nil report graded fresh")
	}
}
