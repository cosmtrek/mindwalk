package judge

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestProgressSchemaAcceptsRepresentativeProgress(t *testing.T) {
	cases := []Progress{
		{Phase: "start", Message: "Starting evaluation…"},
		{Phase: "rubric", Step: "generate", Message: "Drafting task rubric…"},
		{Phase: "rubric", Step: "reuse", Message: "Reusing cached rubric."},
		{Phase: "rubric", Step: "skip", Message: "No task text — skipping rubric."},
		{Phase: "scoring", Step: "complete", Message: "Scoring pass complete."},
		{Phase: "done", Message: "Evaluation complete."},
		{Phase: "error", Step: "fail", Message: "Judge CLI failed."},
	}
	compiler := jsonschema.NewCompiler()

	schema, err := compiler.Compile("../../schema/progress.schema.json")
	if err != nil {
		t.Fatal(err)
	}

	for i, p := range cases {
		document, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}

		var value any
		if err := json.Unmarshal(document, &value); err != nil {
			t.Fatal(err)
		}

		if err := schema.Validate(value); err != nil {
			t.Fatalf("case %d (%+v) violates progress schema: %v\nJSON: %s", i, p, err, document)
		}
	}
}
