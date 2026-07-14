package brain

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cosmtrek/mindwalk/internal/event"
)

func TestStoreCreateSearchCorrectTombstoneAndReplay(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) }
	created, err := store.Create("project/mindwalk", "Durable offsets", "Persist offsets before restart. token=synthetic-private-value", ownerProvenance())
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.Create("project/mindwalk", "Durable offsets", "Persist offsets before restart. token=synthetic-private-value", ownerProvenance())
	if err != nil || duplicate.RecordID != created.RecordID {
		t.Fatalf("duplicate = %#v err=%v", duplicate, err)
	}
	ledger, err := os.ReadFile(filepath.Join(root, "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(ledger, []byte("\n")) != 1 || bytes.Contains(ledger, []byte("synthetic-private-value")) {
		t.Fatalf("ledger duplicate or secret leak: %s", ledger)
	}
	results, err := store.Search("restart offsets", "project/mindwalk", 10)
	if err != nil || len(results) != 1 || results[0].Memory.MemoryID != created.MemoryID {
		t.Fatalf("search = %#v err=%v", results, err)
	}
	corrected, err := store.Correct(created.MemoryID, "Durable cursors", "Resume from the durable ledger sequence.", ownerProvenance())
	if err != nil || corrected.RecordID == created.RecordID {
		t.Fatalf("corrected = %#v err=%v", corrected, err)
	}
	if old, err := store.Search("offsets", "", 10); err != nil || len(old) != 0 {
		t.Fatalf("stale FTS result = %#v err=%v", old, err)
	}
	if current, err := store.Search("ledger sequence", "", 10); err != nil || len(current) != 1 {
		t.Fatalf("corrected FTS result = %#v err=%v", current, err)
	}
	tombstone, err := store.Tombstone(created.MemoryID, ownerProvenance())
	if err != nil || !tombstone.Tombstoned {
		t.Fatalf("tombstone = %#v err=%v", tombstone, err)
	}
	if results, err := store.Search("ledger", "", 10); err != nil || len(results) != 0 {
		t.Fatalf("tombstoned search = %#v err=%v", results, err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	all, err := reopened.List(true)
	if err != nil || len(all) != 1 || !all[0].Tombstoned {
		t.Fatalf("replayed memories = %#v err=%v", all, err)
	}
	markdown, err := os.ReadFile(filepath.Join(root, "markdown", created.MemoryID+".md"))
	if err != nil || !bytes.Contains(markdown, []byte("status: tombstoned")) || bytes.Contains(markdown, []byte("synthetic-private-value")) {
		t.Fatalf("markdown = %q err=%v", markdown, err)
	}
}

func TestStoreRejectsInvalidInputAndTamperedLedger(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("", "title", "body", ownerProvenance()); err == nil {
		t.Fatal("empty namespace accepted")
	}
	if _, err := store.Create("ns", "title", "body", event.Provenance{}); err == nil {
		t.Fatal("missing provenance accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "memory.jsonl"), []byte(`{"tampered":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err == nil {
		t.Fatal("tampered ledger accepted")
	}
}

func ownerProvenance() event.Provenance {
	confidence := float64(1)
	return event.Provenance{SourceType: "owner", SourceName: "local-owner", Quality: event.QualityExact, Confidence: &confidence}
}
