package event

import (
	"os"
	"path/filepath"
	"testing"
)

// countProjector is a deterministic test read model: events per type.
type countProjector struct {
	counts map[string]int
	resets int
}

func (p *countProjector) Name() string { return "count" }
func (p *countProjector) Version() int { return 1 }
func (p *countProjector) Reset() error { p.counts = map[string]int{}; p.resets++; return nil }
func (p *countProjector) Apply(e Envelope) error {
	p.counts[e.EventType]++
	return nil
}

func TestReplayDeterministicAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	for seq := int64(0); seq < 4; seq++ {
		e := synthetic()
		e.Sequence = seq
		if seq%2 == 1 {
			e.EventType = TypeFileEdited
		}
		fin, err := Finalize(e)
		if err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if err := l.Append(fin); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	l.Close()

	p := &countProjector{}
	if err := Replay(path, p); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	first := map[string]int{}
	for k, v := range p.counts {
		first[k] = v
	}
	if first[TypeFileRead] != 2 || first[TypeFileEdited] != 2 {
		t.Fatalf("unexpected projection: %v", first)
	}

	if err := Replay(path, p); err != nil {
		t.Fatalf("second Replay: %v", err)
	}
	if p.resets != 2 {
		t.Fatalf("Reset ran %d times, want 2", p.resets)
	}
	for k, v := range first {
		if p.counts[k] != v {
			t.Fatalf("replay not idempotent for %s: %d vs %d", k, p.counts[k], v)
		}
	}
}

func TestReplaySkipsQuarantinedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	fin, err := Finalize(synthetic())
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if err := l.Append(fin); err != nil {
		t.Fatalf("Append: %v", err)
	}
	l.Close()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("garbage line\n")
	f.Close()

	p := &countProjector{}
	if err := Replay(path, p); err != nil {
		t.Fatalf("Replay over damaged ledger: %v", err)
	}
	if p.counts[TypeFileRead] != 1 {
		t.Fatalf("verified event not projected: %v", p.counts)
	}
}

func TestReplayMissingLedgerIsEmpty(t *testing.T) {
	p := &countProjector{}
	if err := Replay(filepath.Join(t.TempDir(), "absent.jsonl"), p); err != nil {
		t.Fatalf("Replay of missing ledger: %v", err)
	}
	if len(p.counts) != 0 {
		t.Fatalf("events invented from a missing ledger: %v", p.counts)
	}
}
