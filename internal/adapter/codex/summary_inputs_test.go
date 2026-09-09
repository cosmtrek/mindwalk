package codex

import (
	"path/filepath"
	"testing"
)

func TestSummaryInputsDeclaresTitleIndex(t *testing.T) {
	a := Adapter{Dir: filepath.Join("root", "sessions")}
	inputs := a.SummaryInputs("any.jsonl")

	want := filepath.Join("root", "session_index.jsonl")
	if len(inputs) == 0 || inputs[0] != want {
		t.Fatalf("SummaryInputs = %v, want first candidate %s", inputs, want)
	}

	override := Adapter{Dir: "root", IndexPath: filepath.Join("custom", "idx.jsonl")}
	if inputs := override.SummaryInputs("any.jsonl"); len(inputs) != 1 || inputs[0] != override.IndexPath {
		t.Fatalf("IndexPath override not honored: %v", inputs)
	}
}
