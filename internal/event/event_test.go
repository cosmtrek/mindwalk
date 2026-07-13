package event

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAllTypesValidAndUnique(t *testing.T) {
	if len(AllTypes) != 51 {
		t.Fatalf("AllTypes has %d entries, want 51 per the event model", len(AllTypes))
	}
	seen := map[string]bool{}
	for _, typ := range AllTypes {
		if seen[typ] {
			t.Fatalf("duplicate event type %q", typ)
		}
		seen[typ] = true
		e := synthetic()
		e.EventType = typ
		if _, err := Finalize(e); err != nil {
			t.Fatalf("canonical type %q rejected: %v", typ, err)
		}
	}
}

func TestOptionalDistinguishableFromEmpty(t *testing.T) {
	missing := synthetic()
	missing.RepoID = nil
	empty := synthetic()
	emptyVal := ""
	empty.RepoID = &emptyVal

	a, err := Finalize(missing)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	b, err := Finalize(empty)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if a.EventID == b.EventID {
		t.Fatal("missing repoId and recorded-empty repoId share an identity")
	}

	bj, _ := json.Marshal(b)
	if !strings.Contains(string(bj), `"repoId":""`) {
		t.Fatalf("recorded-empty repoId lost in serialization: %s", bj)
	}
	var back Envelope
	if err := json.Unmarshal(bj, &back); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if back.RepoID == nil || *back.RepoID != "" {
		t.Fatal("round-trip lost the recorded-empty repoId")
	}
	aj, _ := json.Marshal(a)
	var backA Envelope
	if err := json.Unmarshal(aj, &backA); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if backA.RepoID != nil {
		t.Fatal("round-trip invented a repoId that was never recorded")
	}
}

func TestRawContentNeverRequired(t *testing.T) {
	e := synthetic()
	e.Provenance.RawEventHash = nil
	e.Provenance.Quality = QualityEstimated
	fin, err := Finalize(e)
	if err != nil {
		t.Fatalf("envelope without raw hash rejected: %v", err)
	}
	if fin.EventID == "" {
		t.Fatal("no identity computed without raw content")
	}
}

func TestRedactedFieldsCarry(t *testing.T) {
	e := synthetic()
	e.RedactedFields = []string{"attrs.command"}
	e.Provenance.Quality = QualityRedacted
	fin, err := Finalize(e)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	j, _ := json.Marshal(fin)
	if !strings.Contains(string(j), `"redactedFields":["attrs.command"]`) {
		t.Fatalf("redactedFields lost: %s", j)
	}
}

func TestDerivedRequiresExplanation(t *testing.T) {
	e := synthetic()
	e.Provenance.Quality = QualityDerived
	if _, err := Finalize(e); err == nil {
		t.Fatal("derived quality without explanation was accepted")
	}
	e.Provenance.Explanation = "parent inferred from adjacent subagent launch"
	conf := 0.8
	e.Provenance.Confidence = &conf
	if _, err := Finalize(e); err != nil {
		t.Fatalf("valid derived envelope rejected: %v", err)
	}
}
