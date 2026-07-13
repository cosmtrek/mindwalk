package review

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/agents"
	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestAnalyzeCompareAndMarkdownAreDeterministicAndTruthful(t *testing.T) {
	left := fixtureTrace("left", []model.Event{
		{Seq: 0, Action: "edit", Targets: []model.Target{{Path: "a.go", Touch: "edit"}}},
		{Seq: 1, Action: "verify", IsError: true, Targets: []model.Target{}},
		{Seq: 2, Action: "edit", Targets: []model.Target{{Path: "a.go", Touch: "edit"}}, Outside: []model.OutsideTouch{{Scope: "tmp", Path: "[REDACTED]"}}},
		{Seq: 3, Action: "edit", Targets: []model.Target{{Path: "a.go", Touch: "edit"}}},
	})
	right := fixtureTrace("right", []model.Event{{Seq: 0, Action: "read", Targets: []model.Target{{Path: "a.go", Touch: "read"}}}, {Seq: 1, Action: "read", Targets: []model.Target{{Path: "b.go", Touch: "read"}}}})
	processes := []agents.Process{{ID: "agt_fixture"}, {ID: "agt_child"}}
	review := Analyze(left, processes)
	if review.Errors != 1 || review.Verifications != 1 || review.EditsAfterLastVerify != 2 || len(review.ChurnFiles) != 1 || review.ScopeDriftTouches != 1 || review.AgentProcesses != 2 {
		t.Fatalf("review = %#v", review)
	}
	comparison := Compare(left, right, processes, nil)
	if !reflect.DeepEqual(comparison.SharedFiles, []string{"a.go"}) || !reflect.DeepEqual(comparison.OnlyRight, []string{"b.go"}) || comparison.MemoryStatus != "UNAVAILABLE" {
		t.Fatalf("comparison = %#v", comparison)
	}
	if second := Compare(left, right, processes, nil); !reflect.DeepEqual(comparison, second) {
		t.Fatal("comparison is not deterministic")
	}
	packet := Markdown(review)
	for _, expected := range []string{"# Mindwalk owner review: left", "2 edit(s) after last verification", "`a.go`", "Provenance: derived"} {
		if !strings.Contains(packet, expected) {
			t.Fatalf("packet missing %q: %s", expected, packet)
		}
	}
}

func fixtureTrace(id string, events []model.Event) *model.Trace {
	return &model.Trace{Version: 1, Session: model.TraceSession{ID: id}, Events: events, Marks: []model.Mark{}}
}
