package event

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// finalized returns a finalized synthetic envelope distinguished by seq.
func finalized(t *testing.T, seq int64) Envelope {
	t.Helper()
	e := synthetic()
	e.Sequence = seq
	fin, err := Finalize(e)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	return fin
}

func TestLogAppendReadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	defer l.Close()
	var want []string
	for seq := int64(0); seq < 5; seq++ {
		e := finalized(t, seq)
		if err := l.Append(e); err != nil {
			t.Fatalf("Append seq %d: %v", seq, err)
		}
		want = append(want, e.EventID)
	}
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i, e := range got {
		if e.EventID != want[i] {
			t.Fatalf("event %d out of order: got %s, want %s", i, e.EventID, want[i])
		}
	}
}

func TestLogRejectsDuplicatesAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	e := finalized(t, 1)
	if err := l.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := l.Append(e); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate append: got %v, want ErrDuplicate", err)
	}
	l.Close()

	re, err := OpenLog(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer re.Close()
	if !re.Contains(e.EventID) {
		t.Fatal("seen index lost across reopen")
	}
	if err := re.Append(e); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate after reopen: got %v, want ErrDuplicate", err)
	}
	if re.Len() != 1 {
		t.Fatalf("Len = %d, want 1", re.Len())
	}
}

func TestLogRejectsUnverifiedEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	defer l.Close()
	if err := l.Append(synthetic()); err == nil {
		t.Fatal("unfinalized envelope accepted")
	}
	tampered := finalized(t, 1)
	tampered.Attrs["tool"] = "tampered"
	if err := l.Append(tampered); !errors.Is(err, ErrIdentity) {
		t.Fatalf("tampered envelope: got %v, want ErrIdentity", err)
	}
	if got, _ := ReadAll(path); len(got) != 0 {
		t.Fatalf("rejected envelopes reached the ledger: %d events", len(got))
	}
}

func TestLogTornTailRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	good := finalized(t, 1)
	if err := l.Append(good); err != nil {
		t.Fatalf("Append: %v", err)
	}
	l.Close()

	// Simulate a crash mid-write: a final line with no newline.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"schemaVersion":1,"eventType":"file.read","occ`)
	f.Close()

	re, err := OpenLog(path)
	if err != nil {
		t.Fatalf("reopen after torn write: %v", err)
	}
	defer re.Close()
	next := finalized(t, 2)
	if err := re.Append(next); err != nil {
		t.Fatalf("Append after recovery: %v", err)
	}
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 2 || got[0].EventID != good.EventID || got[1].EventID != next.EventID {
		t.Fatalf("recovered ledger wrong: %d events", len(got))
	}
	q, err := os.ReadFile(path + ".quarantine")
	if err != nil {
		t.Fatalf("quarantine file: %v", err)
	}
	if !strings.Contains(string(q), "torn final line") {
		t.Fatalf("torn line not preserved in quarantine: %s", q)
	}
	if strings.Contains(string(q), `"occ`) {
		t.Fatalf("raw torn content persisted in quarantine: %s", q)
	}
}

func TestLogQuarantinesMalformedAndOversizedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	good := finalized(t, 1)
	if err := l.Append(good); err != nil {
		t.Fatalf("Append: %v", err)
	}
	l.Close()

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("not json at all\n")
	f.WriteString(strings.Repeat("x", MaxLineBytes+1) + "\n")
	f.Close()

	re, err := OpenLog(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer re.Close()
	if re.Len() != 1 {
		t.Fatalf("Len = %d, want 1 verified event", re.Len())
	}
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 1 || got[0].EventID != good.EventID {
		t.Fatalf("good event lost among quarantined lines: %d events", len(got))
	}
	q, _ := os.ReadFile(path + ".quarantine")
	if c := strings.Count(string(q), "\n"); c != 2 {
		t.Fatalf("quarantine has %d records, want 2", c)
	}
	if strings.Contains(string(q), "not json at all") || strings.Contains(string(q), strings.Repeat("x", 32)) {
		t.Fatal("raw rejected line persisted in quarantine")
	}
}

func TestLogNeverRewritesExistingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	defer l.Close()
	if err := l.Append(finalized(t, 1)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	before, _ := os.ReadFile(path)
	if err := l.Append(finalized(t, 2)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(after), string(before)) {
		t.Fatal("append rewrote existing ledger bytes")
	}
}

func TestOpenLogAtConfinesStorageToDataRoot(t *testing.T) {
	root := t.TempDir()
	l, err := OpenLogAt(root, filepath.Join("ledger", "events.jsonl"))
	if err != nil {
		t.Fatalf("OpenLogAt: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "ledger", "events.jsonl")); err != nil {
		t.Fatalf("confined ledger missing: %v", err)
	}

	outside := t.TempDir()
	if _, err := OpenLogAt(root, filepath.Join("..", filepath.Base(outside), "escape.jsonl")); !errors.Is(err, ErrUnsafeLogPath) {
		t.Fatalf("traversal accepted: %v", err)
	}
	symlink := filepath.Join(root, "escape")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLogAt(root, filepath.Join("escape", "events.jsonl")); !errors.Is(err, ErrUnsafeLogPath) {
		t.Fatalf("symlink escape accepted: %v", err)
	}
}
