package claudecode

import (
	"path/filepath"
	"testing"
)

func TestSummaryInputsDeclaresAgentSidecarOnly(t *testing.T) {
	a := Adapter{}
	child := filepath.Join("proj", "agent-abc.jsonl")
	inputs := a.SummaryInputs(child)

	want := filepath.Join("proj", "agent-abc.meta.json")
	if len(inputs) != 1 || inputs[0] != want {
		t.Fatalf("SummaryInputs(%q) = %v, want [%s]", child, inputs, want)
	}

	if inputs := a.SummaryInputs(filepath.Join("proj", "root.jsonl")); inputs != nil {
		t.Fatalf("main session declared sidecars: %v", inputs)
	}
}
