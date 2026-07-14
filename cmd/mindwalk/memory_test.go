package main

import (
	"path/filepath"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/brain"
)

func TestMemoryCLIAddSearchCorrectAndTombstone(t *testing.T) {
	data := filepath.Join(t.TempDir(), "brain")
	if err := memoryCmd([]string{"add", "-data-dir", data, "-namespace", "project/test", "-title", "Local search", "-body", "SQLite FTS fixture"}); err != nil {
		t.Fatal(err)
	}
	store, err := brain.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	memories, err := store.List(false)
	if err != nil || len(memories) != 1 {
		t.Fatalf("memories = %#v err=%v", memories, err)
	}
	if err := memoryCmd([]string{"search", "-data-dir", data, "SQLite"}); err != nil {
		t.Fatal(err)
	}
	if err := memoryCmd([]string{"correct", "-data-dir", data, "-title", "Corrected", "-body", "durable search", memories[0].MemoryID}); err != nil {
		t.Fatal(err)
	}
	if err := memoryCmd([]string{"tombstone", "-data-dir", data, memories[0].MemoryID}); err != nil {
		t.Fatal(err)
	}
	if active, err := store.List(false); err != nil || len(active) != 0 {
		t.Fatalf("active = %#v err=%v", active, err)
	}
}
